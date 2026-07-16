package daemon

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startPokeServer stands up a Server wired with a real HookEventStore plus any
// stores/closures the caller needs, so a test can subscribe to the store and
// observe whether a daemon-executed change pokes the board.
func startPokeServer(t *testing.T, token string, configure func(*Server)) (string, *HookEventStore) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	hooks := NewHookEventStore()
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		CmdFactory: echoCmd,
		Logger:     zerolog.Nop(),
		HookEvents: hooks,
	}
	if configure != nil {
		configure(srv)
	}
	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr
	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if derr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(cancel)
	return addr, hooks
}

// assertPoked asserts a board poke reaches a subscriber within a second.
func assertPoked(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("subscriber should have been notified of the daemon-executed change")
	}
}

// assertNotPoked asserts no board poke arrives, so a failed mutation does not
// trigger a needless board refetch.
func assertNotPoked(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("subscriber must not be notified when the mutation failed")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCloseTicket_NotifiesBoard(t *testing.T) {
	token := "tok"
	addr, hooks := startPokeServer(t, token, func(s *Server) {
		s.CloseTicketer = func(CloseTicketRequest) error { return nil }
	})
	ch := hooks.Subscribe()
	defer hooks.Unsubscribe(ch)

	body, _ := json.Marshal(CloseTicketRequest{PMKey: "SC-1"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"close-ticket", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)
	assertPoked(t, ch)
}

func TestCloseTicket_FailureDoesNotNotify(t *testing.T) {
	token := "tok"
	addr, hooks := startPokeServer(t, token, func(s *Server) {
		s.CloseTicketer = func(CloseTicketRequest) error { return errors.WithDetails("boom") }
	})
	ch := hooks.Subscribe()
	defer hooks.Unsubscribe(ch)

	body, _ := json.Marshal(CloseTicketRequest{PMKey: "SC-1"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"close-ticket", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assertNotPoked(t, ch)
}

func TestBoardTransition_NotifiesBoard(t *testing.T) {
	token := "tok"
	addr, hooks := startPokeServer(t, token, func(s *Server) {
		s.BoardTransitioner = func(BoardTransitionRequest) error { return nil }
	})
	ch := hooks.Subscribe()
	defer hooks.Unsubscribe(ch)

	body, _ := json.Marshal(BoardTransitionRequest{})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"board-transition", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)
	assertPoked(t, ch)
}

func TestConfirmOpApproval_NotifiesBoard(t *testing.T) {
	token := "tok"
	addr, hooks := startPokeServer(t, token, func(s *Server) {
		s.PendingConfirms = NewPendingConfirmStore()
	})

	// Queue a destructive operation.
	resp := sendRequest(t, addr, Request{Token: token, ClientPID: 1111, ConfirmID: "cid-1", Args: []string{"jira", "issue", "delete", "KAN-1"}})
	require.True(t, resp.AwaitConfirm)

	// Subscribe only now so the queue submission itself does not muddy the read.
	ch := hooks.Subscribe()
	defer hooks.Unsubscribe(ch)

	// Approve from a distinct client — approval alone must poke the board.
	opResp := sendRequest(t, addr, Request{Token: token, ClientPID: 2222, Args: []string{"confirm-op", "cid-1", "yes"}})
	assert.Equal(t, "ok\n", opResp.Stdout)
	assertPoked(t, ch)
}

func TestDestructiveConfirm_RedemptionNotifiesBoard(t *testing.T) {
	token := "tok"
	addr, hooks := startPokeServer(t, token, func(s *Server) {
		s.PendingConfirms = NewPendingConfirmStore()
	})

	// Queue and approve.
	resp := sendRequest(t, addr, Request{Token: token, ClientPID: 1111, ConfirmID: "cid-2", Args: []string{"jira", "issue", "delete", "KAN-1"}})
	require.True(t, resp.AwaitConfirm)
	opResp := sendRequest(t, addr, Request{Token: token, ClientPID: 2222, Args: []string{"confirm-op", "cid-2", "yes"}})
	require.Equal(t, "ok\n", opResp.Stdout)

	// Subscribe, then redeem the grant by resubmitting the granted command.
	ch := hooks.Subscribe()
	defer hooks.Unsubscribe(ch)

	sendRequest(t, addr, Request{Token: token, ClientPID: 1111, ConfirmID: "cid-2", Args: []string{"jira", "issue", "delete", "KAN-1"}})
	assertPoked(t, ch)
}
