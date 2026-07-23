//go:build !windows

package cmddaemon

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func bs(size int64, unixSec int64) binStat {
	return binStat{size: size, mtime: time.Unix(unixSec, 0)}
}

// TestWatchStateDebounce covers the pure decision logic: a change is only
// trusted after it holds stable for one observation.
func TestWatchStateDebounce(t *testing.T) {
	var st watchState
	v0, v1, v2 := bs(10, 100), bs(20, 200), bs(30, 300)

	if a := st.observe(v0); a != actionWait { // seed baseline
		t.Fatalf("baseline observe = %v, want wait", a)
	}
	if a := st.observe(v0); a != actionWait { // unchanged
		t.Fatalf("unchanged observe = %v, want wait", a)
	}
	if a := st.observe(v1); a != actionWait { // first sighting of new build
		t.Fatalf("first-change observe = %v, want wait", a)
	}
	if a := st.observe(v2); a != actionWait { // still changing (mid-write) → not stable
		t.Fatalf("still-changing observe = %v, want wait", a)
	}
	if a := st.observe(v2); a != actionHandover { // held stable → act
		t.Fatalf("stable observe = %v, want handover", a)
	}
}

// TestWatchStatePostponeRetries proves a returned handover that the caller
// declines (postpone) is offered again on the next identical reading, while a
// reject suppresses it until the binary changes again.
func TestWatchStatePostponeRetries(t *testing.T) {
	var st watchState
	v0, v1 := bs(10, 100), bs(20, 200)
	st.observe(v0)
	st.observe(v1) // candidate
	if a := st.observe(v1); a != actionHandover {
		t.Fatalf("observe = %v, want handover", a)
	}
	// Caller postpones (does nothing to state); the same reading re-offers.
	if a := st.observe(v1); a != actionHandover {
		t.Fatalf("post-postpone observe = %v, want handover again", a)
	}
	// Caller rejects; now the same build is not offered again.
	st.reject(v1)
	if a := st.observe(v1); a != actionWait {
		t.Fatalf("post-reject observe = %v, want wait", a)
	}
	// A genuinely new build resumes the cycle.
	v2 := bs(30, 300)
	st.observe(v2)
	if a := st.observe(v2); a != actionHandover {
		t.Fatalf("new-build observe = %v, want handover", a)
	}
}

// statSource is a concurrency-safe, test-controlled stat provider.
type statSource struct {
	mu  sync.Mutex
	cur binStat
}

func (s *statSource) set(b binStat) { s.mu.Lock(); s.cur = b; s.mu.Unlock() }
func (s *statSource) get(string) (binStat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, nil
}

func newTestCoordinator(src *statSource) (*handoverCoordinator, *atomic.Int32, *atomic.Bool) {
	var blocking atomic.Int32
	var reexeced atomic.Bool
	c := &handoverCoordinator{
		logger:      zerolog.Nop(),
		execPath:    "/fake/human",
		interval:    time.Millisecond,
		statOf:      src.get,
		sanity:      func(context.Context, string) error { return nil },
		blockingOps: func() int { return int(blocking.Load()) },
		reexec: func(context.Context, *handoverCoordinator) error {
			reexeced.Store(true)
			return nil
		},
	}
	return c, &blocking, &reexeced
}

func TestWatchHandsOverOnStableRebuild(t *testing.T) {
	src := &statSource{}
	src.set(bs(10, 100))
	c, _, reexeced := newTestCoordinator(src)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.watch(ctx); close(done) }()

	time.Sleep(20 * time.Millisecond) // let the baseline settle across several ticks
	src.set(bs(20, 200))              // a rebuild

	select {
	case <-done: // watch returns after a successful handover
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not hand over on a stable rebuild")
	}
	if !reexeced.Load() {
		t.Fatal("reexec was not invoked")
	}
}

func TestWatchPostponesWhileBlocked(t *testing.T) {
	src := &statSource{}
	src.set(bs(10, 100))
	c, blocking, reexeced := newTestCoordinator(src)
	blocking.Store(1) // work in flight

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.watch(ctx)

	time.Sleep(20 * time.Millisecond)
	src.set(bs(20, 200)) // rebuild, but blocked

	time.Sleep(100 * time.Millisecond)
	if reexeced.Load() {
		t.Fatal("handover happened while a blocking op was in flight")
	}

	blocking.Store(0) // work finished; the pending rebuild should now be picked up
	deadline := time.Now().Add(2 * time.Second)
	for !reexeced.Load() {
		if time.Now().After(deadline) {
			t.Fatal("handover did not resume after blocking op cleared")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatchSkipsBinaryThatFailsSanity(t *testing.T) {
	src := &statSource{}
	src.set(bs(10, 100))
	c, _, reexeced := newTestCoordinator(src)
	c.sanity = func(context.Context, string) error { return errors.New("broken build") }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.watch(ctx)

	time.Sleep(20 * time.Millisecond)
	src.set(bs(20, 200)) // rebuild that fails sanity

	time.Sleep(150 * time.Millisecond)
	if reexeced.Load() {
		t.Fatal("handed over to a binary that failed the sanity check")
	}
}

func TestWatchStopsOnContextCancel(t *testing.T) {
	src := &statSource{}
	src.set(bs(10, 100))
	c, _, _ := newTestCoordinator(src)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.watch(ctx); close(done) }()
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch did not return after context cancel")
	}
}

func TestDrainInflightWaitsThenProceeds(t *testing.T) {
	var conns atomic.Int64
	conns.Store(2) // two proxied streams still in flight
	c := &handoverCoordinator{
		logger:       zerolog.Nop(),
		drainTimeout: 2 * time.Second,
		activeConns:  func() int64 { return conns.Load() },
	}

	returned := make(chan struct{})
	go func() { c.drainInflight(context.Background()); close(returned) }()

	// Still draining while connections remain.
	select {
	case <-returned:
		t.Fatal("drainInflight returned while connections were still active")
	case <-time.After(150 * time.Millisecond):
	}

	conns.Store(0) // streams finished
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("drainInflight did not return after connections drained")
	}
}

func TestDrainInflightHonorsTimeout(t *testing.T) {
	c := &handoverCoordinator{
		logger:       zerolog.Nop(),
		drainTimeout: 50 * time.Millisecond,
		activeConns:  func() int64 { return 1 }, // never drains
	}
	done := make(chan struct{})
	go func() { c.drainInflight(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drainInflight ignored its timeout with a stuck connection")
	}
}

func TestDrainInflightNilIsNoOp(t *testing.T) {
	c := &handoverCoordinator{logger: zerolog.Nop()}
	c.drainInflight(context.Background()) // must return immediately, no panic
}

func TestWaitHandoverReadySignaled(t *testing.T) {
	r, w, _ := os.Pipe()
	defer func() { _ = r.Close() }()
	go func() {
		_, _ = w.WriteString("ok\n")
		_ = w.Close()
	}()
	if err := waitHandoverReady(context.Background(), r, time.Second); err != nil {
		t.Fatalf("waitHandoverReady = %v, want nil", err)
	}
}

func TestWaitHandoverReadyChildDied(t *testing.T) {
	r, w, _ := os.Pipe()
	defer func() { _ = r.Close() }()
	_ = w.Close() // child exited without signaling → EOF

	err := waitHandoverReady(context.Background(), r, time.Second)
	if err == nil {
		t.Fatal("waitHandoverReady = nil, want error for a child that never signaled")
	}
}

func TestWaitHandoverReadyTimeout(t *testing.T) {
	r, w, _ := os.Pipe()
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }() // never write, never close: force a timeout

	err := waitHandoverReady(context.Background(), r, 50*time.Millisecond)
	if err == nil {
		t.Fatal("waitHandoverReady = nil, want timeout error")
	}
}
