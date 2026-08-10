package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

func hookStoreWithOneEvent(name string) *HookEventStore {
	s := NewHookEventStore()
	s.Append(hookevents.Event{EventName: "PostToolUse", AgentName: name, Timestamp: time.Now()})
	return s
}

// An agent whose connections resolve to nothing is unknown, not idle — the
// central fix of SC-3853.
func TestNewAgentProgressProbe_UnknownWhenAgentUnmapped(t *testing.T) {
	name := "board-SC-1-implementation"
	hooks := hookStoreWithOneEvent(name)
	reg := NewAgentIPRegistry()
	inflight := NewInflightModelRequests()

	probe := NewAgentProgressProbe(hooks, inflight, reg)
	p, ok := probe(name)
	require.True(t, ok)
	assert.Equal(t, ModelRequestUnknown, p.ModelRequest)
}

func TestNewAgentProgressProbe_NoneWhenMappedAndNothingOpen(t *testing.T) {
	name := "board-SC-1-implementation"
	hooks := hookStoreWithOneEvent(name)
	reg := NewAgentIPRegistry()
	reg.Register("10.0.0.1", name)
	inflight := NewInflightModelRequests()

	probe := NewAgentProgressProbe(hooks, inflight, reg)
	p, ok := probe(name)
	require.True(t, ok)
	assert.Equal(t, ModelRequestNone, p.ModelRequest)
}

func TestNewAgentProgressProbe_OpenWhenMappedAndRequestInFlight(t *testing.T) {
	name := "board-SC-1-implementation"
	hooks := hookStoreWithOneEvent(name)
	reg := NewAgentIPRegistry()
	reg.Register("10.0.0.1", name)
	inflight := NewInflightModelRequests()
	inflight.Mark(name, 1)

	probe := NewAgentProgressProbe(hooks, inflight, reg)
	p, ok := probe(name)
	require.True(t, ok)
	assert.Equal(t, ModelRequestOpen, p.ModelRequest)
}

func TestNewAgentProgressProbe_UnknownAgentStaysUnknownToTheProbe(t *testing.T) {
	hooks := NewHookEventStore()
	reg := NewAgentIPRegistry()
	inflight := NewInflightModelRequests()

	probe := NewAgentProgressProbe(hooks, inflight, reg)
	_, ok := probe("board-SC-9-planning")
	assert.False(t, ok)
}

func TestNewAgentProgressProbe_NilHookStore(t *testing.T) {
	probe := NewAgentProgressProbe(nil, NewInflightModelRequests(), NewAgentIPRegistry())
	assert.Nil(t, probe)
}

// NewInflightMarker resolves a mapped connection straight to inflight.
func TestNewInflightMarker_MappedConnectionMarksInflight(t *testing.T) {
	name := "board-SC-1-implementation"
	reg := NewAgentIPRegistry()
	reg.Register("172.17.0.5", name)
	inflight := NewInflightModelRequests()
	pending := NewPendingModelRequests()

	mark := NewInflightMarker(inflight, reg, pending)
	mark("172.17.0.5:9999", 1)

	assert.True(t, inflight.Outstanding(name))
}

// An unmapped connection's mark is HELD, not dropped — the daemon-restart hole
// review round 1 found (SC-3853).
func TestNewInflightMarker_UnmappedConnectionIsHeldNotDropped(t *testing.T) {
	reg := NewAgentIPRegistry()
	inflight := NewInflightModelRequests()
	pending := NewPendingModelRequests()

	mark := NewInflightMarker(inflight, reg, pending)
	mark("172.17.0.5:9999", 1)

	name := "board-SC-1-implementation"
	pending.Replay("172.17.0.5", name, inflight)
	assert.True(t, inflight.Outstanding(name), "the held mark must apply once the mapping resolves")
}
