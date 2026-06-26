package monarch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSink records every event sent to it, safely across goroutines.
type captureSink struct {
	mu     sync.Mutex
	events []Event
}

func (c *captureSink) Send(e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *captureSink) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

// The first heartbeat fires immediately so a daemon appears without waiting a
// full interval, and it carries only the daemon id + idle state (no work).
func TestStartHeartbeat_beatsImmediately(t *testing.T) {
	sink := &captureSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartHeartbeat(ctx, sink, "daemon-abcd", "teamA", time.Hour)

	require.Eventually(t, func() bool { return len(sink.snapshot()) >= 1 }, time.Second, 5*time.Millisecond)
	e := sink.snapshot()[0]
	assert.Equal(t, EventHeartbeat, e.Type)
	assert.Equal(t, "daemon-abcd", e.DaemonID)
	assert.Equal(t, "teamA", e.Team)
	assert.Equal(t, StateIdle, e.State)
	assert.Empty(t, e.AgentID)
	assert.Empty(t, e.Repo)
}

// Cancelling the context stops the heartbeat loop.
func TestStartHeartbeat_stopsOnContextCancel(t *testing.T) {
	sink := &captureSink{}
	ctx, cancel := context.WithCancel(context.Background())

	StartHeartbeat(ctx, sink, "daemon-abcd", "", 5*time.Millisecond)
	require.Eventually(t, func() bool { return len(sink.snapshot()) >= 2 }, time.Second, 2*time.Millisecond)

	cancel()
	time.Sleep(20 * time.Millisecond)
	after := len(sink.snapshot())
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, after, len(sink.snapshot()), "no heartbeats after cancel")
}
