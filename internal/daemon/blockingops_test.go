package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestBlockingOpsCounting(t *testing.T) {
	s := &Server{}
	if got := s.BlockingOps(); got != 0 {
		t.Fatalf("fresh server BlockingOps = %d, want 0", got)
	}

	// While the op runs the count is visible to a concurrent reader (the watcher).
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		s.withBlockingOp(func() {
			close(entered)
			<-release
		})
		close(done)
	}()

	<-entered
	if got := s.BlockingOps(); got != 1 {
		t.Fatalf("during op BlockingOps = %d, want 1", got)
	}
	close(release)
	<-done
	if got := s.BlockingOps(); got != 0 {
		t.Fatalf("after op BlockingOps = %d, want 0", got)
	}
}

func TestBlockingOpsNested(t *testing.T) {
	s := &Server{}
	var wg sync.WaitGroup
	const n = 5
	release := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			s.withBlockingOp(func() { <-release })
			wg.Done()
		}()
	}
	// Spin until all have registered.
	deadline := time.Now().Add(2 * time.Second)
	for s.BlockingOps() != n {
		if time.Now().After(deadline) {
			t.Fatalf("BlockingOps never reached %d (stuck at %d)", n, s.BlockingOps())
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if got := s.BlockingOps(); got != 0 {
		t.Fatalf("after all ops BlockingOps = %d, want 0", got)
	}
}

// TestServerServesInjectedListener proves ListenAndServe uses a pre-bound
// Listener instead of binding s.Addr — the seam the self-restart handover uses
// to pass live sockets to a re-exec'd child.
func TestServerServesInjectedListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &Server{
		Token:    "tok",
		Listener: ln,
		// Addr deliberately points nowhere bindable to prove it is ignored.
		Addr:   "127.0.0.1:1",
		Logger: zerolog.Nop(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- s.ListenAndServe(ctx) }()

	// A request on the injected listener's address must reach the server. An
	// unknown command with a valid token returns a normal error response, not a
	// connection failure — which is all we need to prove routing works.
	addr := ln.Addr().String()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial injected listener: %v", err)
	}
	req := Request{Token: "tok", Protocol: Protocol, Args: []string{"tracker-issues"}}
	line, _ := json.Marshal(req)
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(append(line, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
		t.Fatalf("expected a response from the injected-listener server: %v", err)
	}
	_ = conn.Close()

	cancel()
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("ListenAndServe did not return after context cancel")
	}
}
