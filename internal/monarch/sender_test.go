package monarch

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainAll pops every currently-queued event without blocking.
func drainAll(s *Sender) []Event {
	var out []Event
	for {
		select {
		case e := <-s.ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestSender_Send_dropsOldestWhenFull(t *testing.T) {
	// No server is listening, so the run goroutine stays in its dial/backoff
	// loop and never drains s.ch — letting us observe the drop-oldest behaviour
	// deterministically. A tiny buffer makes "full" trivial to reach.
	s := &Sender{
		addr:   "127.0.0.1:1", // unreachable; nothing drains the channel
		ch:     make(chan Event, 2),
		logger: zerolog.Nop(),
		done:   make(chan struct{}),
		quit:   make(chan struct{}),
	}

	s.Send(Event{DaemonID: "a"})
	s.Send(Event{DaemonID: "b"})
	// Buffer is full (cap 2). The next Send must drop the oldest ("a").
	s.Send(Event{DaemonID: "c"})

	queued := drainAll(s)
	assert.Len(t, queued, 2)
	assert.Equal(t, "b", queued[0].DaemonID, "oldest (a) dropped")
	assert.Equal(t, "c", queued[1].DaemonID, "newest retained")
}

func TestSender_Send_neverBlocksAfterClose(t *testing.T) {
	s := NewSender(context.Background(), "127.0.0.1:1", zerolog.Nop())
	s.Close()
	// Sending after Close must return promptly via the quit case, not block.
	done := make(chan struct{})
	go func() {
		s.Send(Event{DaemonID: "x"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Send blocked after Close")
	}
}

func TestSender_Close_idempotent(t *testing.T) {
	s := NewSender(context.Background(), "127.0.0.1:1", zerolog.Nop())
	assert.NotPanics(t, func() {
		s.Close()
		s.Close()
	})
}

func TestSender_run_deliversToListener(t *testing.T) {
	// Bind first so the address is live before the sender dials — race-free.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewSender(ctx, ln.Addr().String(), zerolog.Nop())
	defer s.Close()

	s.Send(Event{Type: EventAgentStart, DaemonID: "daemon-xyz", State: StateCoding})

	conn, err := ln.Accept()
	require.NoError(t, err)
	require.NotNil(t, conn)
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	require.NoError(t, err)

	var got Event
	require.NoError(t, json.Unmarshal(line, &got))
	assert.Equal(t, "daemon-xyz", got.DaemonID)
	assert.Equal(t, EventAgentStart, got.Type)
}

func TestSender_run_backoffThenClose(t *testing.T) {
	// An unreachable address forces run into its dial-fail/backoff branch; Close
	// must then unblock the backoff sleep and let the goroutine exit promptly.
	s := NewSender(context.Background(), "127.0.0.1:1", zerolog.Nop())
	time.Sleep(20 * time.Millisecond) // let run attempt at least one dial
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not interrupt the backoff sleep")
	}
}

func TestSender_run_stopsOnCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := NewSender(ctx, "127.0.0.1:1", zerolog.Nop())
	cancel()
	// Closing waits on the run goroutine; it must have observed ctx cancel.
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop on ctx cancel")
	}
}

func TestNextBackoff_caps(t *testing.T) {
	assert.Equal(t, 2*backoffInitial, nextBackoff(backoffInitial))
	assert.Equal(t, backoffMax, nextBackoff(backoffMax))
}
