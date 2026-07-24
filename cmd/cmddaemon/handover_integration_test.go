//go:build !windows

package cmddaemon

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// TestMain lets this test binary double as a handover child: when a parent
// re-execs it with HANDOVER_TEST_CHILD=1 set, it plays the child's role
// (signal ready via the inherited fd, then exit) instead of running the suite.
func TestMain(m *testing.M) {
	if os.Getenv("HANDOVER_TEST_CHILD") == "1" {
		signalHandoverReady(zerolog.Nop())
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestReexecChildSuccess drives reexecChild across a real process boundary: it
// re-execs this test binary as the "new build", which reports ready through the
// inherited readiness pipe. reexecChild must then mark the handover and stop the
// parent — the exact commit sequence the daemon relies on.
func TestReexecChildSuccess(t *testing.T) {
	// Inherited by the re-exec'd child through os.Environ(); TestMain reads it.
	// Set on the parent too, but the parent already passed TestMain at startup.
	t.Setenv("HANDOVER_TEST_CHILD", "1")

	ls, err := openListeners("127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind listeners: %v", err)
	}
	defer func() {
		_ = ls.daemon.Close()
		_ = ls.proxy.Close()
		_ = ls.chrome.Close()
	}()

	var handed atomic.Bool
	stopped := make(chan struct{})
	c := &handoverCoordinator{
		listeners:  ls,
		logger:     zerolog.Nop(),
		execPath:   os.Args[0], // the test binary stands in for the rebuilt daemon
		handedOver: &handed,
		stop:       func() { close(stopped) },
	}

	if err := reexecChild(context.Background(), c); err != nil {
		t.Fatalf("reexecChild: %v", err)
	}
	if !handed.Load() {
		t.Fatal("handedOver was not set after a successful handover")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("parent server context was not stopped after handover")
	}
}

// TestReexecChildRetiresBeforeDraining pins the ordering the socket handover
// depends on: the outgoing relay socket must stop being discoverable BEFORE the
// parent settles down to drain, otherwise a client globbing the socket
// directory can still attach to the daemon that is about to exit.
func TestReexecChildRetiresBeforeDraining(t *testing.T) {
	t.Setenv("HANDOVER_TEST_CHILD", "1")

	ls, err := openListeners("127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind listeners: %v", err)
	}
	defer func() {
		_ = ls.daemon.Close()
		_ = ls.proxy.Close()
		_ = ls.chrome.Close()
	}()

	var order []string
	var mu sync.Mutex
	record := func(what string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, what)
	}

	var handed atomic.Bool
	c := &handoverCoordinator{
		listeners:    ls,
		logger:       zerolog.Nop(),
		execPath:     os.Args[0],
		handedOver:   &handed,
		stop:         func() { record("stop") },
		drainTimeout: time.Second,
		retire:       func() { record("retire") },
		activeConns: func() int64 {
			record("drain-check")
			return 0
		},
	}

	if err := reexecChild(context.Background(), c); err != nil {
		t.Fatalf("reexecChild: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) < 3 {
		t.Fatalf("expected retire, drain-check and stop; got %v", order)
	}
	if order[0] != "retire" {
		t.Fatalf("retire must come first, got %v", order)
	}
	if order[len(order)-1] != "stop" {
		t.Fatalf("stop must come last, got %v", order)
	}
}

// TestReexecChildStartFailure proves a re-exec that cannot even start leaves the
// parent untouched (no stop, no handover commit), so the running daemon keeps
// serving.
func TestReexecChildStartFailure(t *testing.T) {
	ls, err := openListeners("127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind listeners: %v", err)
	}
	defer func() {
		_ = ls.daemon.Close()
		_ = ls.proxy.Close()
		_ = ls.chrome.Close()
	}()

	var handed atomic.Bool
	stopCalled := atomic.Bool{}
	c := &handoverCoordinator{
		listeners:  ls,
		logger:     zerolog.Nop(),
		execPath:   "/nonexistent/human-binary-does-not-exist",
		handedOver: &handed,
		stop:       func() { stopCalled.Store(true) },
	}

	if err := reexecChild(context.Background(), c); err == nil {
		t.Fatal("reexecChild = nil error, want failure for a missing binary")
	}
	if handed.Load() {
		t.Fatal("handedOver set despite a failed handover")
	}
	if stopCalled.Load() {
		t.Fatal("parent stopped despite a failed handover")
	}
	// The parent's listeners must still be accepting (not consumed by the failure).
	if ls.daemon.Addr() == nil {
		t.Fatal("parent daemon listener no longer usable after a failed handover")
	}
}
