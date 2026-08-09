package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// StageWaitHeader records the interval a card spent waiting between one stage
// finishing and the next launching (SC-2462). Like ClaimHeader / PlanCommentHeader
// / CloseFailedHeader it is content, NOT a stage transition: it MUST never join
// orderedMarkerSpecs, so ClassifyMarker never sees it and it never moves a card.
// Its body carries the eligibility anchor, the measured wait, and the cause, so a
// wait is attributable and distinguishable from a stall by reading the ticket.
const StageWaitHeader = "[human:" + MarkerStageWait + "]"

// Body-line prefixes for a stage-wait marker, following the `key: value`
// convention of ClaimStagePrefix / the pr:/branch: handoff lines.
const (
	StageWaitStagePrefix    = "stage:"
	StageWaitEligiblePrefix = "eligible:"
	StageWaitWaitedPrefix   = "waited:"
	StageWaitCausePrefix    = "cause:"
)

// WaitCause names what filled the gap between a stage becoming eligible and its
// launch, so a change intended to shorten waits can be shown to have worked. An
// empty cause means the launch was human-initiated (a manual board drop) — that
// interval is deliberation, not a pipeline wait, and is never recorded.
type WaitCause string

const (
	// WaitCauseChain: the live SessionEnd fix→review chain launched this stage.
	// Normally sub-threshold and therefore silent; a large gap here means something
	// upstream (a claim, a gate) held the launch.
	WaitCauseChain WaitCause = "chain"
	// WaitCausePollBoundary: the durable reconcile pass launched this stage because
	// the live chain never fired (a daemon restart lost the Stop event) — the exact
	// cause of the 31-minute hole in SC-2462. The wait is bounded by the reconcile
	// interval plus the time the live trigger was absent.
	WaitCausePollBoundary WaitCause = "poll boundary"
	// WaitCauseRetry: an in-place retry/rework relaunch drove this stage.
	WaitCauseRetry WaitCause = "retry"
)

// StageWaitThreshold is the shortest inter-stage gap worth recording. Below it a
// stage is treated as promptly chained and NO record is posted, so a healthy run
// produces no noise ("a record of waiting, not a heartbeat"). It sits above the
// ordinary reconcile tick (BoardReconcileInterval, 2m + jitter) so a single
// durable-pass hop is not mistaken for a wait, and well below StuckRunningGrace.
// A package var so tests can pin it.
var StageWaitThreshold = 5 * time.Minute

// stageWaitNow is the clock the wait recorder reads, indirected so tests can pin
// it, mirroring claimNow.
var stageWaitNow = time.Now

// eligibleAnchor returns the Created time of the newest done-state marker on the
// thread — the moment the previous stage finished and this stage became eligible
// to run. ok is false when no stage has completed yet (no anchor to measure a
// wait against, e.g. a first launch out of Backlog).
func eligibleAnchor(comments []tracker.Comment) (time.Time, bool) {
	var newest tracker.Comment
	found := false
	for _, c := range comments {
		_, state, ok := ClassifyMarker(c.Body)
		if !ok || state != BoardDone {
			continue
		}
		if !found || commentNewer(c, newest) {
			newest = c
			found = true
		}
	}
	if !found {
		return time.Time{}, false
	}
	return newest.Created, true
}

// composeStageWait builds the marker body (without the daemon stamp). The field
// order is the wire format its readers already parse by prefix, so it is stated
// explicitly rather than left to the map's iteration order.
func composeStageWait(stage BoardStage, eligibleAt, startedAt time.Time, cause WaitCause) string {
	return markerBody(marker.Marker{
		Type: MarkerStageWait,
		Fields: fields(
			"stage", string(stage),
			"eligible", eligibleAt.UTC().Format(time.RFC3339),
			"waited", startedAt.Sub(eligibleAt).Round(time.Second).String(),
			"cause", string(cause),
		),
	}, "stage", "eligible", "waited", "cause")
}

// recordStageWait posts a [human:stage-wait] marker when the gap between the
// previous stage finishing and this launch exceeds StageWaitThreshold and the
// launch was NOT human-initiated (empty cause). It is best-effort: a post failure
// is logged and swallowed so it never blocks the launch it merely annotates.
// Returns whether a record was posted (for callers/tests that assert on it).
func recordStageWait(ctx context.Context, commenter tracker.Commenter, pmKey string, stage BoardStage, comments []tracker.Comment, cause WaitCause, daemonID string, logger zerolog.Logger) bool {
	if strings.TrimSpace(string(cause)) == "" {
		return false // human-initiated: deliberation, not a pipeline wait
	}
	eligibleAt, ok := eligibleAnchor(comments)
	if !ok {
		return false // no prior-stage completion to measure a wait against
	}
	now := stageWaitNow()
	if now.Sub(eligibleAt) < StageWaitThreshold {
		return false // promptly chained: stay silent
	}
	body := composeStageWait(stage, eligibleAt, now, cause)
	if _, err := commenter.AddComment(ctx, pmKey, body); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board stage-wait: could not post wait record")
		return false
	}
	return true
}

// latestStageWaitCause returns the cause line of the newest [human:stage-wait]
// marker on the thread, or "" when none — the stuck-running pass reads it so a
// stall that followed a recorded wait is attributable, not judged from silence.
func latestStageWaitCause(comments []tracker.Comment) string {
	return latestPrefixedLine(comments, StageWaitHeader, StageWaitCausePrefix)
}
