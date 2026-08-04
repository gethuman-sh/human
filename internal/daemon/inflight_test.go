package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// SC-3074: a request sent to the model and not yet answered must read as
// outstanding, and a matching completion must clear it.
func TestInflightModelRequests_MarkAndOutstanding(t *testing.T) {
	m := NewInflightModelRequests()
	assert.False(t, m.Outstanding("board-SC-3074-implementation"), "nothing marked yet")

	m.Mark("board-SC-3074-implementation", 1)
	assert.True(t, m.Outstanding("board-SC-3074-implementation"))

	m.Mark("board-SC-3074-implementation", -1)
	assert.False(t, m.Outstanding("board-SC-3074-implementation"))

	// An extra decrement (e.g. a response read failing after a delta the read
	// never actually observed) must clamp at zero, never go negative and be
	// misread as "very outstanding".
	m.Mark("board-SC-3074-implementation", -1)
	assert.False(t, m.Outstanding("board-SC-3074-implementation"))
}

func TestInflightModelRequests_MultipleAgentsAreIndependent(t *testing.T) {
	m := NewInflightModelRequests()
	m.Mark("a", 1)
	assert.True(t, m.Outstanding("a"))
	assert.False(t, m.Outstanding("b"), "an unrelated agent must not be affected")
}

func TestInflightModelRequests_NilAndEmptySafe(t *testing.T) {
	var m *InflightModelRequests
	assert.NotPanics(t, func() {
		m.Mark("a", 1)
	})
	assert.False(t, m.Outstanding("a"))

	live := NewInflightModelRequests()
	live.Mark("", 1)
	assert.False(t, live.Outstanding(""), "an empty agent name is a no-op, not a wildcard")
}
