package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAgentIPRegistry_AttributeKnownIP(t *testing.T) {
	r := NewAgentIPRegistry()
	r.Register("172.17.0.4", agentNameFor("SC-2555", BoardImplementation))

	ticket, stage, ok := r.Attribute("172.17.0.4:44120")
	assert.True(t, ok)
	assert.Equal(t, "SC-2555", ticket)
	assert.Equal(t, string(BoardImplementation), stage)
}

func TestAgentIPRegistry_AttributeUnknownIP(t *testing.T) {
	r := NewAgentIPRegistry()
	_, _, ok := r.Attribute("10.9.9.9:55000")
	assert.False(t, ok, "an unknown source is unattributed, not an error")
}

func TestAgentIPRegistry_AttributeBareIP(t *testing.T) {
	r := NewAgentIPRegistry()
	r.Register("172.17.0.5", agentNameFor("SC-100", BoardPlanning))
	// A remote address without a port still resolves.
	ticket, stage, ok := r.Attribute("172.17.0.5")
	assert.True(t, ok)
	assert.Equal(t, "SC-100", ticket)
	assert.Equal(t, string(BoardPlanning), stage)
}

func TestAgentIPRegistry_NonAgentNameUnattributed(t *testing.T) {
	r := NewAgentIPRegistry()
	r.Register("172.17.0.6", "not-a-board-agent")
	_, _, ok := r.Attribute("172.17.0.6:1")
	assert.False(t, ok, "a name parseAgentName cannot decode is unattributed")
}

func TestAgentIPRegistry_Unregister(t *testing.T) {
	r := NewAgentIPRegistry()
	name := agentNameFor("SC-7", BoardImplementation)
	r.Register("172.17.0.7", name)
	r.Unregister("172.17.0.7")
	_, _, ok := r.Attribute("172.17.0.7:2")
	assert.False(t, ok)
}

// AgentFor resolves the raw agent name — the key InflightModelRequests uses —
// distinct from Attribute's decoded (ticket, stage) pair (SC-3074).
func TestAgentIPRegistry_AgentFor(t *testing.T) {
	r := NewAgentIPRegistry()
	name := agentNameFor("SC-3074", BoardImplementation)
	r.Register("172.17.0.9", name)

	got, ok := r.AgentFor("172.17.0.9:44120")
	assert.True(t, ok)
	assert.Equal(t, name, got)

	// A bare IP without a port still resolves, matching Attribute.
	got, ok = r.AgentFor("172.17.0.9")
	assert.True(t, ok)
	assert.Equal(t, name, got)
}

func TestAgentIPRegistry_AgentForUnknownIP(t *testing.T) {
	r := NewAgentIPRegistry()
	_, ok := r.AgentFor("10.9.9.9:55000")
	assert.False(t, ok, "an unknown source is unattributed, not an error")
}

func TestAgentIPRegistry_AgentForNilSafe(t *testing.T) {
	var r *AgentIPRegistry
	_, ok := r.AgentFor("1.2.3.4:5")
	assert.False(t, ok)
}

func TestAgentIPRegistry_NilAndEmptySafe(t *testing.T) {
	var r *AgentIPRegistry
	assert.NotPanics(t, func() {
		r.Register("1.2.3.4", "board-x-y")
		r.Unregister("1.2.3.4")
	})
	_, _, ok := r.Attribute("1.2.3.4:5")
	assert.False(t, ok)

	// Empty ip/name are ignored rather than planting a bogus mapping.
	live := NewAgentIPRegistry()
	live.Register("", "board-x-y")
	live.Register("1.2.3.4", "")
	assert.Empty(t, live.byIP)
}
