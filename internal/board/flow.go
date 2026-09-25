package board

import (
	"time"

	"github.com/gethuman-sh/human/internal/daemon"
)

// FlowStallAfter is how long the WHOLE pipeline may produce nothing before the
// board says so. A var, not a const, so tests can pin it — the same shape as
// daemon.StuckRunningGrace, which is the per-run equivalent.
//
// Two hours is roughly eight times StuckRunningGrace: long enough that a slow
// agent run plus its retries never trips it, short enough that a stoppage is
// noticed within the same working session rather than the next morning.
var FlowStallAfter = 2 * time.Hour

// FlowKeySample caps how many in-flight tickets the signal names. Enough to
// point at the work, few enough that the strip stays one readable line.
const FlowKeySample = 3

// flowInFlightStates are the states in which the MACHINE owes an advance:
// it took the work on and nothing has come back. A card resting in Backlog is
// waiting for a person, which is not a stall and must never read as one.
var flowInFlightStates = map[string]bool{
	string(daemon.BoardRunning): true,
	string(daemon.BoardQueued):  true,
	string(daemon.BoardOutage):  true,
}

// AssessFlow answers "is the pipeline producing anything" from the marker trail
// the cards already carry, and from nothing else.
//
// Deliberately blind to containers, processes and leases: the failure this
// exists to catch is work that is marked running while its ticket records no
// motion, which every liveness probe reports as healthy. The most recent marker
// ANYWHERE on the board is the pipeline's pulse — one card wedged beside others
// that are advancing is a card problem, not a stopped line.
//
// now is injected rather than read so the classification is testable without a
// clock.
func AssessFlow(cards []daemon.BoardViewCard, now time.Time) daemon.BoardFlow {
	var progressAt time.Time
	var inFlight []string
	unreadable := 0
	for _, c := range cards {
		at, ok := parseCardProgress(c.StageEnteredAt)
		if ok && at.After(progressAt) {
			progressAt = at
		}
		// A card held by a sequencing answer (WaitsFor != "") started nothing and
		// owes no advance until the ticket it waits for is done — the same carve-out
		// AgentNamesForCard makes for the analogous liveness judgement
		// (internal/daemon/board_liveness.go), for the same reason: without it a
		// queued card deliberately parked on a person's decision reads exactly like
		// one whose launch died, and the stall it causes can never clear on its own.
		pending := flowInFlightStates[c.State] && c.WaitsFor == ""
		if pending {
			inFlight = append(inFlight, c.Key)
		}
		// A card we could not read might be the one that is advancing, or the
		// one that is wedged; either way it is not evidence for a claim.
		if c.Degraded || (pending && !ok) {
			unreadable++
		}
	}

	// Nothing was asked of the machine and every card was readable: quiet, and
	// correct however long ago the last thing shipped.
	if len(inFlight) == 0 && unreadable == 0 {
		return daemon.BoardFlow{State: daemon.BoardFlowIdle, Since: formatStageTime(progressAt)}
	}

	flow := daemon.BoardFlow{
		Since:      formatStageTime(progressAt),
		InFlight:   len(inFlight),
		Keys:       sampleKeys(inFlight),
		Unreadable: unreadable,
	}

	// Something advanced recently — that is the answer, whatever else is on the
	// board. This is also the automatic clear: one new marker and the claim goes.
	if !progressAt.IsZero() && now.Sub(progressAt) < FlowStallAfter {
		flow.State = daemon.BoardFlowFlowing
		return flow
	}

	// Past here nothing has advanced for FlowStallAfter (or nothing observable
	// exists at all), so the only question left is whether we can trust that.
	if unreadable > 0 || progressAt.IsZero() {
		flow.State = daemon.BoardFlowUnknown
		return flow
	}
	flow.State = daemon.BoardFlowStalled
	return flow
}

// parseCardProgress reads a card's marker timestamp. An absent or malformed
// value is reported as unusable rather than as a zero time, so a card the board
// cannot date is never mistaken for one that last moved in year zero.
func parseCardProgress(stageEnteredAt string) (time.Time, bool) {
	if stageEnteredAt == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, stageEnteredAt)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// sampleKeys trims the named tickets to FlowKeySample; the count on the signal
// still reports the true total.
func sampleKeys(keys []string) []string {
	if len(keys) <= FlowKeySample {
		return keys
	}
	return keys[:FlowKeySample]
}
