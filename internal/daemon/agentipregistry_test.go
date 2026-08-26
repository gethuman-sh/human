package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// Mapped answers the question the liveness probe must ask before trusting an
// in-flight count of zero (SC-3853).
// A per-ticket auxiliary run is not a board stage, but it spends real money on
// a real ticket: without this its outcome arrives unattributed and the cost
// ledger drops it (SC-4608).
func TestAttribute_AuxAgentName(t *testing.T) {
	r := NewAgentIPRegistry()
	r.Register("10.0.0.5", "idea-draft-SC-1")
	r.Register("10.0.0.6", "relate-SC-1")
	r.Register("10.0.0.7", "garbage-SC-1")

	ticket, stage, ok := r.Attribute("10.0.0.5:1")
	require.True(t, ok)
	assert.Equal(t, "SC-1", ticket)
	assert.Equal(t, "idea-draft", stage, "the prefix is the stage for a run that has none")

	ticket, stage, ok = r.Attribute("10.0.0.6:1")
	require.True(t, ok)
	assert.Equal(t, "SC-1", ticket)
	assert.Equal(t, "relate", stage)

	_, _, ok = r.Attribute("10.0.0.7:1")
	assert.False(t, ok, "the aux grammar answers for a closed list, not for any hyphenated name")
}

func TestAgentIPRegistry_Mapped(t *testing.T) {
	r := NewAgentIPRegistry()
	name := agentNameFor("SC-1", BoardImplementation)
	r.Register("1.2.3.4", name)

	assert.True(t, r.Mapped(name))
	assert.False(t, r.Mapped(agentNameFor("SC-2", BoardPlanning)))
	assert.False(t, r.Mapped(""))

	var nilReg *AgentIPRegistry
	assert.False(t, nilReg.Mapped(name))
}

// Retain drops mappings whose agent fell out of the running set, which is what
// stops a recycled bridge IP from attributing a new agent's traffic to a dead
// one (SC-3853).
func TestAgentIPRegistry_RetainDropsDeadAgents(t *testing.T) {
	r := NewAgentIPRegistry()
	a := agentNameFor("SC-1", BoardImplementation)
	b := agentNameFor("SC-2", BoardPlanning)
	r.Register("1.2.3.4", a)
	r.Register("5.6.7.8", b)

	r.Retain(map[string]struct{}{a: {}})

	name, ok := r.AgentFor("1.2.3.4")
	require.True(t, ok)
	assert.Equal(t, a, name)
	_, ok = r.AgentFor("5.6.7.8")
	assert.False(t, ok)
	assert.False(t, r.Mapped(b))
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
		r.Retain(nil)
		r.Mapped("board-x-y")
	})
	_, _, ok := r.Attribute("1.2.3.4:5")
	assert.False(t, ok)

	// Empty ip/name are ignored rather than planting a bogus mapping.
	live := NewAgentIPRegistry()
	live.Register("", "board-x-y")
	live.Register("1.2.3.4", "")
	assert.Empty(t, live.byIP)
}
