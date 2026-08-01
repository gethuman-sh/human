package claude

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// SC-2595: the preflight agent must escalate an ordering DECISION REQUIRED only
// when an overlapping ticket is actively in progress — the one case where two
// live runs can collide. Overlap with open-but-unstarted work (planned or
// dormant backlog) must be recorded, not escalated. Before the fix the prompt
// ordered on the single predicate "still open", which halted the run for a
// dormant backlog ticket that could never collide.
func TestPreflight_OrdersOnlyInProgressOverlap(t *testing.T) {
	body := string(preflightAgentContent)

	// The bug: the escalation criterion was "still open" / "open ticket".
	assert.NotContains(t, body, "still open",
		"ordering must not escalate on the not-closed predicate; it conflates in-progress with dormant backlog")
	assert.NotContains(t, body, "two open tickets aimed at the same code are a fork",
		"an open-but-unstarted overlap is not a fork")

	// The fix: the criterion consults Status and orders only on work in flight.
	assert.Contains(t, body, "in progress",
		"the ordering criterion must key on work actively in progress")
	assert.Contains(t, body, "Status",
		"the prompt must tell the agent to read each hit's Status from the search JSON")

	// Open-but-unstarted overlap is recorded, not escalated.
	lower := strings.ToLower(body)
	assert.Contains(t, lower, "record it",
		"overlap with not-yet-started work must be recorded rather than escalated")
	assert.Contains(t, lower, "not started",
		"the prompt must distinguish not-yet-started overlap from in-progress overlap")

	// The protection this check exists for — two live runs on one thing — is
	// preserved: that case is always in flight, so it is still a fork.
	assert.Contains(t, body, "two live runs",
		"the parallel-duplicate-implementation collision must still be named as a fork")
}
