package claude

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// SC-2595: the preflight agent must escalate an ordering DECISION REQUIRED only
// when work on an overlapping ticket is really underway — the one case where two
// live runs can collide. Overlap with dormant backlog must be recorded, not
// escalated. Before that fix the prompt ordered on the single predicate "still
// open", which halted the run for a backlog ticket that could never collide.
//
// SC-2648 kept that rule and replaced how it is decided: ticket Status was a
// proxy for "underway" and wrong in both directions, so the criterion is now
// live forge state. The assertions moved with the mechanism; what they protect
// did not.
func TestPreflight_OrdersOnlyOnWorkReallyUnderway(t *testing.T) {
	body := string(preflightAgentContent)
	lower := strings.ToLower(body)

	// The bug: the escalation criterion was "still open" / "open ticket".
	assert.NotContains(t, body, "still open",
		"ordering must not escalate on the not-closed predicate; it conflates in-progress with dormant backlog")
	assert.NotContains(t, body, "two open tickets aimed at the same code are a fork",
		"an open-but-unstarted overlap is not a fork")

	// The criterion is what the forge reports, not what the ticket says.
	assert.Contains(t, body, "human underway <OTHER_KEY>",
		"a candidate collision must be confirmed against the forge before it orders anything")
	assert.Contains(t, body, "human underway <PM_KEY>",
		"the run must first ask whether this very ticket is already being built")

	// Wording overlap with nothing open against it is recorded, not escalated.
	assert.Contains(t, lower, "does not stop the run",
		"a ticket that merely overlaps in wording must not halt the run")

	// The protection this check exists for — a second copy of work someone is
	// already holding open — is named explicitly.
	assert.Contains(t, body, "Do not build a second copy",
		"an open PR or branch for this ticket must stop a duplicate implementation")

	// The forge cannot see a claimed run that has not branched yet, so the
	// marker signal has to stay.
	assert.Contains(t, body, "[human:claim]",
		"a claimed run with no branch yet is underway and must still be treated as such")
}
