package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// deployFixRoundThread is the marker trail of n deploy-fixer rounds that
// already ran on this ticket — the state deployFailedOrDispatchFixer reads to
// decide both whether the budget has room and what the card gets to say.
func deployFixRoundThread(n int) []tracker.Comment {
	base := time.Now().Add(-time.Hour)
	out := make([]tracker.Comment, 0, n)
	for i := range n {
		out = append(out, cmt("[human:deploy-fix-started]\nCI checks failed on the pull request (failing: frontend-test)\npr: u\nnumber: 7\nbranch: feat/x",
			base.Add(time.Duration(i)*time.Minute)))
	}
	return out
}

func failDeployAfterRounds(t *testing.T, c *fakeCommenter, deps BoardTransitionDeps) string {
	t.Helper()
	err := deps.deployFailedOrDispatchFixer(context.Background(), "SC-1",
		PRResult{Number: 7, URL: "u"},
		"CI checks failed on the pull request (failing: frontend-test)", nil, "feat/x", false)
	require.Error(t, err, "the fallback reports the deploy failure it just posted")
	body, ok := posted(c, DeployFailedHeader)
	require.True(t, ok, "the budget-exhausted path must red the card")
	return body
}

// SC-3640: the budget-exhausted fallback is the message the SC-3508 card wore
// while two rounds had already run — it read exactly like a card nothing had
// touched. It now states the count, and still names the failing check.
func TestDeployFailedOrDispatchFixer_BudgetExhaustedStatesTheRounds(t *testing.T) {
	c := &fakeCommenter{comments: deployFixRoundThread(DefaultDeployFixRounds)}
	deps, l := reviewableDeps(c, &fakeDeployer{})

	body := failDeployAfterRounds(t, c, deps)

	assert.Contains(t, body, "(failing: frontend-test)", "SC-3615's check name must survive the suffix")
	assert.Contains(t, body, "2 automated fix rounds ran before this.")
	assert.Zero(t, l.calls, "the budget is spent — no third round is dispatched")
}

// A failure with no rounds behind it claims none: the CLI path (no launcher)
// keeps today's wording byte-for-byte.
func TestDeployFailedOrDispatchFixer_FirstFailureClaimsNoRounds(t *testing.T) {
	c := &fakeCommenter{}
	deps := bareDeps(c, &fakeDeployer{})

	body := failDeployAfterRounds(t, c, deps)

	assert.Contains(t, body, "reason: CI checks failed on the pull request (failing: frontend-test)")
	assert.NotContains(t, body, "automated fix round", "no automation ran, so the card claims none")
}

// A thread that cannot be read leaves the count at zero rather than guessing —
// and the failure is still posted, because the deploy did fail.
func TestDeployFailedOrDispatchFixer_CommentReadErrorClaimsNoRounds(t *testing.T) {
	c := &fakeCommenter{}
	deps, _ := reviewableDeps(c, &fakeDeployer{})
	deps.Commenter = listErrCommenter{c}

	err := deps.deployFailedOrDispatchFixer(context.Background(), "SC-1",
		PRResult{Number: 7, URL: "u"},
		"CI checks failed on the pull request (failing: frontend-test)", nil, "feat/x", false)

	require.Error(t, err)
	body, ok := posted(c, DeployFailedHeader)
	require.True(t, ok, "an unreadable thread must not swallow the deploy failure")
	assert.NotContains(t, body, "automated fix round")
}

// Unchanged behaviour: with room left in the budget the fallback still
// dispatches a round instead of redding the card.
func TestDeployFailedOrDispatchFixer_BudgetWithRoomStillDispatches(t *testing.T) {
	c := &fakeCommenter{comments: deployFixRoundThread(1)}
	deps, l := reviewableDeps(c, &fakeDeployer{})

	require.NoError(t, deps.deployFailedOrDispatchFixer(context.Background(), "SC-1",
		PRResult{Number: 7, URL: "u"},
		"CI checks failed on the pull request (failing: frontend-test)", nil, "feat/x", false))

	assert.Equal(t, 1, l.calls, "a round with budget left is dispatched")
	_, red := posted(c, DeployFailedHeader)
	assert.False(t, red, "a dispatched round keeps the card spinning, not red")
}
