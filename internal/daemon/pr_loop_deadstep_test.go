package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

// SC-5554: the incident. A daemon restart reaped the reviewer mid round 2, so
// the round's own verdict was never written and the loop read round 1's
// leftover — recorded, not fresh. That is a DEAD STEP, not a racing writer:
// the write is never coming, and re-running the round is what every ordinary
// board stage does. Escalating it redded a card whose PR was green and
// mergeable and cost a person a Retry deploy.
func TestEvaluatePRLoop_deadReviewerWithAPriorRoundsRecord_relaunchesTheRound(t *testing.T) {
	comments := []tracker.Comment{
		cmt(PRReviewStartedHeader, time.Unix(1000, 0)),
		cmt(PRFixStartedHeader, time.Unix(2000, 0)),
		cmt(PRReviewStartedHeader, time.Unix(3000, 0)),
	}
	outcome := PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true, ReviewStale: true, StepDead: true}

	assert.Equal(t, PRActionRelaunch, EvaluatePRLoop(comments, outcome),
		"a verdict that was never written is a dead step; the round re-runs")
}

// Round 1's shape of the same death: nothing recorded at all.
func TestEvaluatePRLoop_deadReviewerWithNoRecord_relaunchesTheRound(t *testing.T) {
	comments := []tracker.Comment{
		cmt(PRReviewStartedHeader, time.Unix(1000, 0)),
	}
	outcome := PRLoopOutcome{StepDead: true}

	assert.Equal(t, PRActionRelaunch, EvaluatePRLoop(comments, outcome),
		"no record at all, agent confirmed gone: relaunch the round")
}

// The fixer half, dead with a previous round's report still in the store.
func TestEvaluatePRLoop_deadFixer_relaunchesTheFixer(t *testing.T) {
	comments := []tracker.Comment{
		cmt(PRReviewStartedHeader, time.Unix(1000, 0)),
		cmt(PRFixStartedHeader, time.Unix(2000, 0)),
	}
	outcome := PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true,
		FixRecorded: true, FixStale: true, StepDead: true}

	assert.Equal(t, PRActionRelaunch, EvaluatePRLoop(comments, outcome),
		"the fixer half of the split relaunches the same way")
}

// SC-2378 is NOT weakened: a record the reader could not confirm, with the
// step's agent NOT confirmed gone, still escalates rather than being acted on.
func TestEvaluatePRLoop_staleRecordWithNoConfirmedDeath_stillEscalates(t *testing.T) {
	comments := []tracker.Comment{
		cmt(PRReviewStartedHeader, time.Unix(1000, 0)),
		cmt(PRFixStartedHeader, time.Unix(2000, 0)),
		cmt(PRReviewStartedHeader, time.Unix(3000, 0)),
	}
	outcome := PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true, ReviewStale: true, StepDead: false}

	assert.Equal(t, PRActionEscalate, EvaluatePRLoop(comments, outcome),
		"a write that may still be coming must not be re-run out from under it")
}

// A record that IS this round's and cannot be decoded is a step that decided
// something the daemon cannot read — escalate, never re-run.
func TestEvaluatePRLoop_unreadableFreshRecord_escalates(t *testing.T) {
	comments := []tracker.Comment{
		cmt(PRReviewStartedHeader, time.Unix(1000, 0)),
	}
	outcome := PRLoopOutcome{ReviewRecorded: true, ReviewUnreadable: true, StepDead: true}

	assert.Equal(t, PRActionEscalate, EvaluatePRLoop(comments, outcome),
		"a decoded-but-invalid record is not a dead step; re-running it would spend the budget on the same unreadable sentence")
}

// The relaunch is bounded: at DefaultPRReviewRounds charged review rounds the
// dead step reds instead of re-running.
func TestEvaluatePRLoop_deadReviewerAtTheRoundBudget_escalates(t *testing.T) {
	comments := make([]tracker.Comment, 0, DefaultPRReviewRounds)
	for i := 0; i < DefaultPRReviewRounds; i++ {
		comments = append(comments, cmt(PRReviewStartedHeader, time.Unix(int64(1000*(i+1)), 0)))
	}
	outcome := PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true, ReviewStale: true, StepDead: true}

	assert.Equal(t, PRActionEscalate, EvaluatePRLoop(comments, outcome),
		"the round budget bounds the relaunch just as it bounds an ordinary fix round")
}

// The fix step is bounded by ITS OWN charged count: chargedPRReviewRounds does
// not move when a fixer is relaunched, so bounding the fixer by it would not
// bound it at all (AD4).
func TestEvaluatePRLoop_deadFixerAtTheFixBudget_escalates(t *testing.T) {
	comments := []tracker.Comment{
		cmt(PRReviewStartedHeader, time.Unix(1000, 0)),
	}
	for i := 0; i < DefaultPRReviewRounds; i++ {
		comments = append(comments, cmt(PRFixStartedHeader, time.Unix(int64(2000+1000*i), 0)))
	}
	outcome := PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true,
		FixRecorded: true, FixStale: true, StepDead: true}

	assert.Equal(t, PRActionEscalate, EvaluatePRLoop(comments, outcome),
		"chargedPRReviewRounds does not move for a fix round; the fixer must be bounded by its own count or it relaunches forever")
}

// An outage-refunded round is not charged, so a dead step after an outage still
// has room (SC-5627's refund holds for this bound too).
func TestEvaluatePRLoop_deadReviewerAfterAnOutagedRound_relaunches(t *testing.T) {
	comments := make([]tracker.Comment, 0, DefaultPRReviewRounds+1)
	for i := 0; i < DefaultPRReviewRounds; i++ {
		comments = append(comments, cmt(PRReviewStartedHeader, time.Unix(int64(1000*(i+1)), 0)))
	}
	comments = append(comments, cmt(DeployOutageHeader, time.Unix(int64(1000*DefaultPRReviewRounds+1), 0)))
	outcome := PRLoopOutcome{ReviewVerdict: PRVerdictApproved, ReviewRecorded: true, ReviewStale: true, StepDead: true}

	assert.Equal(t, PRActionRelaunch, EvaluatePRLoop(comments, outcome),
		"the outage refunds the last round, so the dead-step relaunch still has room")
}
