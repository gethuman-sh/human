package daemon

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// testInhibitInterval is short so the ticker-driven loop advances many times
// within a test's Eventually budget without wall-clock cost.
const testInhibitInterval = 2 * time.Millisecond

// fakeLister returns whatever the injected fn yields for the current call
// number, letting a test model "agents on the first tick, none on a later one".
type fakeLister struct {
	mu    sync.Mutex
	calls int
	fn    func(call int) ([]AgentInfo, error)
}

func (f *fakeLister) RunningAgents() ([]AgentInfo, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	fn := f.fn
	f.mu.Unlock()
	return fn(call)
}

func constAgents(agents []AgentInfo) *fakeLister {
	return &fakeLister{fn: func(int) ([]AgentInfo, error) { return agents, nil }}
}

// fakeInhibitor records Acquire/release calls and can be forced to fail every
// Acquire so the loud-log-once behaviour is observable.
type fakeInhibitor struct {
	mu           sync.Mutex
	acquireCalls int
	releaseCalls int
	acquireErr   error
}

func (f *fakeInhibitor) Acquire(_, _ string) (func() error, error) {
	f.mu.Lock()
	f.acquireCalls++
	err := f.acquireErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() error {
		f.mu.Lock()
		f.releaseCalls++
		f.mu.Unlock()
		return nil
	}, nil
}

func (f *fakeInhibitor) acquires() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquireCalls
}

func (f *fakeInhibitor) releases() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releaseCalls
}

// safeBuffer is a mutex-guarded io.Writer so the loop goroutine and the test
// goroutine can write/read the captured log without a data race.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// alwaysEnabled is the common toggle for tests that never flip it.
func alwaysEnabled() bool { return true }

func oneAgent() []AgentInfo { return []AgentInfo{{Name: "agent-1", ContainerID: "c1"}} }

func TestRunSleepInhibitor_AcquiresWhenAgentsRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lister := constAgents(oneAgent())
	inh := &fakeInhibitor{}

	go RunSleepInhibitor(ctx, lister, inh, alwaysEnabled, testInhibitInterval, zerolog.Nop())

	assert.Eventually(t, func() bool { return inh.acquires() == 1 }, time.Second, testInhibitInterval)
	assert.Equal(t, 0, inh.releases(), "block must still be held while the agent runs")
}

func TestRunSleepInhibitor_ReleasesWhenAgentsGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// One agent on the first tick, none afterwards.
	lister := &fakeLister{fn: func(call int) ([]AgentInfo, error) {
		if call == 1 {
			return oneAgent(), nil
		}
		return nil, nil
	}}
	inh := &fakeInhibitor{}

	go RunSleepInhibitor(ctx, lister, inh, alwaysEnabled, testInhibitInterval, zerolog.Nop())

	assert.Eventually(t, func() bool { return inh.releases() == 1 }, time.Second, testInhibitInterval)
	assert.Equal(t, 1, inh.acquires(), "exactly one acquire preceded the release")
}

func TestRunSleepInhibitor_NoDoubleAcquire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lister := constAgents(oneAgent())
	inh := &fakeInhibitor{}

	go RunSleepInhibitor(ctx, lister, inh, alwaysEnabled, testInhibitInterval, zerolog.Nop())

	assert.Eventually(t, func() bool { return inh.acquires() == 1 }, time.Second, testInhibitInterval)
	// Let many further ticks fire; the held block must not be re-acquired.
	time.Sleep(30 * testInhibitInterval)
	assert.Equal(t, 1, inh.acquires(), "a held block must never be re-acquired")
}

func TestRunSleepInhibitor_DisabledReleases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lister := constAgents(oneAgent())
	inh := &fakeInhibitor{}
	var on atomic.Bool
	on.Store(true)

	go RunSleepInhibitor(ctx, lister, inh, on.Load, testInhibitInterval, zerolog.Nop())

	assert.Eventually(t, func() bool { return inh.acquires() == 1 }, time.Second, testInhibitInterval)
	on.Store(false)
	assert.Eventually(t, func() bool { return inh.releases() == 1 }, time.Second, testInhibitInterval)
	// While disabled, no re-acquire even though agents keep running.
	time.Sleep(20 * testInhibitInterval)
	assert.Equal(t, 1, inh.acquires(), "the toggle being off must suppress re-acquire")
}

func TestRunSleepInhibitor_DisabledNeverAcquires(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lister := constAgents(oneAgent())
	inh := &fakeInhibitor{}

	go RunSleepInhibitor(ctx, lister, inh, func() bool { return false }, testInhibitInterval, zerolog.Nop())

	time.Sleep(30 * testInhibitInterval)
	assert.Equal(t, 0, inh.acquires(), "an off toggle must never acquire a block")
}

func TestRunSleepInhibitor_AcquireErrorLoggedOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lister := constAgents(oneAgent())
	inh := &fakeInhibitor{acquireErr: errors.New("boom")}
	var logs safeBuffer
	logger := zerolog.New(&logs)

	go RunSleepInhibitor(ctx, lister, inh, alwaysEnabled, testInhibitInterval, logger)

	// Acquire is retried every tick while it keeps failing (nothing is held).
	assert.Eventually(t, func() bool { return inh.acquires() > 1 }, time.Second, testInhibitInterval)
	// ...but the loud error is emitted exactly once per failure streak.
	time.Sleep(20 * testInhibitInterval)
	assert.Equal(t, 1, strings.Count(logs.String(), `"level":"error"`),
		"the acquire failure must be logged loudly but only once per streak")
	assert.Equal(t, 0, inh.releases(), "a failed acquire holds no block to release")
}

func TestRunSleepInhibitor_ListErrorKeepsBlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Agent on the first tick (→ acquire), a list error on every later tick.
	lister := &fakeLister{fn: func(call int) ([]AgentInfo, error) {
		if call == 1 {
			return oneAgent(), nil
		}
		return nil, errors.New("list failed")
	}}
	inh := &fakeInhibitor{}

	go RunSleepInhibitor(ctx, lister, inh, alwaysEnabled, testInhibitInterval, zerolog.Nop())

	assert.Eventually(t, func() bool { return inh.acquires() == 1 }, time.Second, testInhibitInterval)
	// Through the ensuing list errors the block must be retained.
	time.Sleep(20 * testInhibitInterval)
	assert.Equal(t, 0, inh.releases(), "a transient list error must not drop protection")
}

func TestRunSleepInhibitor_ContextCancelReleases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lister := constAgents(oneAgent())
	inh := &fakeInhibitor{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunSleepInhibitor(ctx, lister, inh, alwaysEnabled, testInhibitInterval, zerolog.Nop())
	}()

	assert.Eventually(t, func() bool { return inh.acquires() == 1 }, time.Second, testInhibitInterval)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunSleepInhibitor did not return after context cancellation")
	}
	assert.Equal(t, 1, inh.releases(), "cancellation must release the held block via deferred cleanup")
}

func TestRunSleepInhibitor_NilDeps(t *testing.T) {
	// Any nil dependency must make the loop a no-op that returns immediately,
	// never a panic.
	inh := &fakeInhibitor{}
	lister := constAgents(oneAgent())

	RunSleepInhibitor(context.Background(), nil, inh, alwaysEnabled, testInhibitInterval, zerolog.Nop())
	RunSleepInhibitor(context.Background(), lister, nil, alwaysEnabled, testInhibitInterval, zerolog.Nop())
	RunSleepInhibitor(context.Background(), lister, inh, nil, testInhibitInterval, zerolog.Nop())

	assert.Equal(t, 0, inh.acquires(), "nil deps must short-circuit before any acquire")
}
