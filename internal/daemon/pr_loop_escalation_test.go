package daemon

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// escalationBody runs the loop to escalation and returns the failed marker body.
func escalationBody(t *testing.T, outcome PRLoopOutcome, diagnose BoardFailureDiagnoser) string {
	t.Helper()
	return escalationBodyAfterRounds(t, 1, outcome, diagnose)
}

// escalationBodyAfterRounds is escalationBody with an explicit number of
// completed review rounds, for the verdicts that only escalate once the round
// budget is spent.
func escalationBodyAfterRounds(t *testing.T, rounds int, outcome PRLoopOutcome, diagnose BoardFailureDiagnoser) string {
	t.Helper()
	c := &fakeCommenter{comments: reviewStartedComments(rounds, "https://example/pr/7", 7, "feat/x")}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	deps.Diagnose = diagnose

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", outcome))

	body, ok := posted(c, PRReviewFailedHeader)
	require.True(t, ok, "the loop must escalate")
	return body
}

// A step that recorded NOTHING did not decide anything — it died, or never got
// far enough to report. Telling a human the outcome was "unreadable" sent them
// to read a review that was never written; the marker must say what was missing
// (SC-1892).
func TestPREscalation_unrecordedVerdictSaysWhatWasMissing(t *testing.T) {
	body := escalationBody(t, PRLoopOutcome{ReviewRecorded: false}, nil)

	assert.Contains(t, body, "PR reviewer")
	assert.Contains(t, body, "stopped before recording a verdict")
	assert.NotContains(t, body, "could not classify",
		"nothing was found, so nothing failed to be classified")
}

// The other half: a step that DID record something the daemon cannot classify
// decided something deliberate, and must not be reported as having died.
func TestPREscalation_unclassifiableVerdictIsNotReportedAsMissing(t *testing.T) {
	body := escalationBody(t, PRLoopOutcome{ReviewVerdict: "maybe?", ReviewRecorded: true}, nil)

	assert.Contains(t, body, "could not classify")
	assert.NotContains(t, body, "without recording",
		"an outcome WAS recorded — reporting it as absent misdirects the reader")
}

// SC-3024: the PR-loop escalation's returned line is always house-style
// situation+next-action — never the raw post-mortem headline/detail a
// diagnoser produced. A raw diagnosis (container/OOM/exit-code vocabulary) is
// never THE message a card-facing marker shows; the ordinary stage-failure
// evidence path is where a diagnosis like this belongs, not the PR-loop
// handover.
func TestPREscalation_unrecordedStepReasonIsHouseStyleNotRawDiagnosis(t *testing.T) {
	diagnose := func(agentName, errorType string) FailureDiagnosis {
		assert.Equal(t, "board-SC-1-prreview", agentName)
		assert.Equal(t, "oom", errorType)
		return FailureDiagnosis{Headline: "the container ran out of memory", Detail: "killed at 4.0GiB"}
	}

	body := escalationBody(t, PRLoopOutcome{
		ReviewRecorded: false,
		Agent:          "board-SC-1-prreview",
		ErrorType:      "oom",
	}, diagnose)

	assert.NotContains(t, body, "the container ran out of memory", "a raw diagnosis headline is never THE message")
	assert.NotContains(t, body, "killed at 4.0GiB", "a raw diagnosis detail is never THE message")
	assert.Contains(t, body, "PR reviewer")
	assert.Contains(t, body, "re-run Deploy", "the escalation names the next action")
}

// The durable reconcile re-drive has no exiting agent to attribute, so it must
// still produce the honest generic line rather than a diagnosis of nothing.
func TestPREscalation_reDriveWithoutAnAgentStillExplainsItself(t *testing.T) {
	diagnose := func(string, string) FailureDiagnosis {
		t.Fatal("a re-drive has no run to diagnose and must not attempt one")
		return FailureDiagnosis{}
	}

	body := escalationBody(t, PRLoopOutcome{ReviewRecorded: false}, diagnose)

	assert.Contains(t, body, "stopped before recording a verdict")
}

// An empty-handed diagnoser must not blank the marker: the fallback line still
// names what was missing.
func TestPREscalation_emptyDiagnosisFallsBackToTheMissingOutcome(t *testing.T) {
	diagnose := func(string, string) FailureDiagnosis { return FailureDiagnosis{} }

	body := escalationBody(t, PRLoopOutcome{
		ReviewRecorded: false,
		Agent:          "board-SC-1-prreview",
	}, diagnose)

	assert.Contains(t, body, "stopped before recording a verdict")
}

// Recorded verdicts the daemon DOES understand keep their existing, more
// specific headlines — the unrecorded case must not swallow them.
func TestPREscalation_recognisedVerdictsKeepTheirHeadlines(t *testing.T) {
	unreviewable := escalationBody(t, PRLoopOutcome{ReviewVerdict: PRVerdictUnreviewable, ReviewRecorded: true}, nil)
	assert.Contains(t, unreviewable, "could not be reviewed")

	// changes-requested reaches escalation only once the round budget is spent.
	budgetSpent := escalationBodyAfterRounds(t, DefaultPRReviewRounds,
		PRLoopOutcome{ReviewVerdict: PRVerdictChanges, ReviewRecorded: true}, nil)
	assert.Contains(t, budgetSpent, "did not converge within the round budget")
}
