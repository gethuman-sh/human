package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
)

// composedOptionsBody renders a decision block through the real composer, which
// returns the marker and its field order as one answer.
func composedOptionsBody(stage BoardStage, context string, opts []BoardOption) string {
	m, order := optionsMarker(stage, context, opts)
	return markerBody(m, order...)
}

// TestDaemonPostedMarkersSatisfyTheirContract walks every marker type the
// daemon writes, composes it the way the daemon composes it, and puts the
// rendered body back through the protocol's own reader and validator.
//
// This is the test the funnel exists for. postMarker logs and posts anyway when
// a marker fails validation — dropping it would stall the card silently — so
// nothing at runtime refuses a malformed marker, and without this the daemon
// could go on posting deploy-failed markers with no reason and deployed markers
// that never said how the work shipped, exactly as it did.
//
// Composing through the real builders is the point: a test that hand-wrote the
// bodies would validate its own strings and pass while the daemon posted
// something else.
func TestDaemonPostedMarkersSatisfyTheirContract(t *testing.T) {
	eligible := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		markerType string
		body       string
	}{
		{MarkerDeployed, DeployedBody("https://example/pr/7")},
		{MarkerDeployed, markerBody(marker.Marker{
			Type:   MarkerDeployed,
			Fields: fields("merged", "already in the base branch; no new PR opened"),
		})},
		{MarkerDeployFailed, markerBody(failureMarker(MarkerDeployFailed,
			"CI checks failed on the pull request — fix the failing checks, then re-run Deploy\n\nfailing: frontend-test"))},
		{MarkerNeedsPlanning, markerBody(failureMarker(MarkerNeedsPlanning, needsPlanningReason))},
		{MarkerPRReviewFailed, markerBody(failureMarker(MarkerPRReviewFailed,
			"could not launch the PR review agent — no container runtime"))},
		{MarkerReviewFailed, markerBody(failureMarker(MarkerReviewFailed, genericStageFailure))},
		{MarkerPipeline, markerBody(marker.Marker{Type: MarkerPipeline, Fields: fields("kind", "fix")})},
		{MarkerOptions, composedOptionsBody(BoardImplementation, "the fixer needs a decision", []BoardOption{
			{ID: "1", Label: "Rebuild the branch"},
			{ID: "2", Label: "Take it over by hand"},
		})},
		{MarkerOptionChosen, markerBody(marker.Marker{Type: MarkerOptionChosen, Head: "1: Rebuild the branch"})},
		{MarkerStageWait, composeStageWait(BoardPlanning, eligible, eligible.Add(31*time.Minute), WaitCausePollBoundary)},
		{MarkerClaim, markerBody(marker.Marker{Type: MarkerClaim, Fields: fields("stage", string(BoardPlanning))})},
		{MarkerCloseFailed, markerBody(failureMarker(MarkerCloseFailed, "the automated close of SC-1 failed: tracker unreachable"))},
		{MarkerRunCancelled, RunCancelledBody(BoardImplementation, "board-SC-1-implementation")},
		{MarkerPlanningFailed, markerBody(failureMarker(failedTypeFor(BoardPlanning), genericStageFailure))},
		{MarkerImplementationFailed, markerBody(failureMarker(failedTypeFor(BoardImplementation), genericStageFailure))},
		{MarkerPlanningOutage, markerBody(pausedOutageMarker(outageTypeFor(BoardPlanning), nil, "", "", "the secret store"))},
		{MarkerImplementationOutage, markerBody(pausedOutageMarker(outageTypeFor(BoardImplementation), nil, "", "", ""))},
		{MarkerReviewOutage, markerBody(pausedOutageMarker(outageTypeFor(BoardVerification), nil, "", "", ""))},
		{MarkerDeployOutage, markerBody(pausedOutageMarker(outageTypeFor(BoardDoneStage), nil, "", "", ""))},
		{MarkerPRReviewStarted, prReviewStartedBody("https://example/pr/7", 7, "feat/x")},
		{MarkerDeployFixStarted, markerBody(marker.Marker{
			Type:   MarkerDeployFixStarted,
			Fields: fields("pr", "https://example/pr/7", "number", "7", "branch", "feat/x"),
			Body:   "CI checks failed on the pull request",
		}, "pr", "number", "branch")},
		{MarkerHandoffCheckUnreadable, markerBody(marker.Marker{
			Type: MarkerHandoffCheckUnreadable,
			Body: "could not verify the handoff for branch feat/x on this machine — git timed out",
		})},
		{MarkerDeployed, ShippedOutOfBandDeployedBody("https://example/pr/7")},
		{MarkerLateResultReconciled, LateResultReconciledBody("review", BoardVerification, eligible, eligible.Add(4*time.Minute))},
	}

	// A decision block only validates when its field order is the one the real
	// composer returns, so the two are taken together rather than re-stated.
	covered := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.markerType, func(t *testing.T) {
			parsed, ok := marker.ParseBody(tc.body)
			require.True(t, ok, "a daemon-posted body must read back as a marker: %q", tc.body)
			assert.Equal(t, tc.markerType, parsed.Type, "the posted body must carry the type it was composed as")
			assert.NoError(t, marker.Validate(parsed),
				"the daemon must not post a marker its own protocol rejects: %q", tc.body)
		})
		covered[tc.markerType] = true
	}

	for _, markerType := range daemonMarkerTypes {
		assert.True(t, covered[markerType],
			"marker type %q is posted by the daemon but no case here checks it against the protocol", markerType)
	}
}

// TestFailureMarkerSplitsHeadlineFromDetail pins the split the card depends on:
// the badge shows one line and the detail pane shows the rest, so the headline
// must survive as the reason field and the detail as prose — and failureBody
// must put them back together in that order. Both halves in one field folds the
// blank line into a continuation and truncates the field block; both in the body
// posts a *-failed marker with no reason at all, which is what the daemon did
// before it went through this builder.
func TestFailureMarkerSplitsHeadlineFromDetail(t *testing.T) {
	body := markerBody(failureMarker(MarkerImplementationFailed,
		"claude exited with code 1: API Error\n\nagent: board-SC-1-implementation\n\nlast output:\n~~~\nboom\n~~~"))

	assert.Equal(t, "claude exited with code 1: API Error", failureReason(body),
		"the card's one-line error is the headline, never the field name or the detail")
	assert.Contains(t, failureBody(body), "last output:", "the detail pane keeps the whole diagnosis")

	parsed, ok := marker.ParseBody(body)
	require.True(t, ok)
	assert.NoError(t, marker.Validate(parsed))
}

// TestFailureBodyReadsMarkersPostedBeforeTheReasonField is the back-compat half:
// markers already on live tickets carry their diagnosis as prose with no reason
// field, and they must keep rendering the same card they always did.
func TestFailureBodyReadsMarkersPostedBeforeTheReasonField(t *testing.T) {
	legacy := ImplementationFailedHeader + "\nclaude exited with code 1: API Error\n\nagent: board-SC-1-implementation"

	assert.Equal(t, "claude exited with code 1: API Error", failureReason(legacy))
	assert.Contains(t, failureBody(legacy), "agent: board-SC-1-implementation")
}
