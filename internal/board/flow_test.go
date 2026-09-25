package board

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/daemon"
)

var flowNow = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// flowCard builds a wire card with the fields AssessFlow reads. A negative ago
// means the card carries no timestamp at all.
func flowCard(key, state string, ago time.Duration) daemon.BoardViewCard {
	c := daemon.BoardViewCard{Key: key, State: state}
	if ago >= 0 {
		c.StageEnteredAt = flowNow.Add(-ago).Format(time.RFC3339)
	}
	return c
}

// The shape the ticket was written for: work marked running while the marker
// trail records no motion at all (SC-3577).
func TestAssessFlow_StalledWhenNothingAdvances(t *testing.T) {
	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "running", 14*time.Hour)}, flowNow)

	assert.Equal(t, daemon.BoardFlowStalled, flow.State)
	assert.Equal(t, 1, flow.InFlight)
	assert.Equal(t, []string{"SC-1"}, flow.Keys)
	assert.Equal(t, flowNow.Add(-14*time.Hour).Format(time.RFC3339), flow.Since)
	assert.Zero(t, flow.Unreadable)
}

// Pipeline motion is board-wide: one wedged card beside cards that are moving is
// a card problem, not a stopped line (AD2).
func TestAssessFlow_FlowingWhenSomethingJustMoved(t *testing.T) {
	flow := AssessFlow([]daemon.BoardViewCard{
		flowCard("SC-1", "running", 14*time.Hour),
		flowCard("SC-2", "done", 5*time.Minute),
	}, flowNow)

	assert.Equal(t, daemon.BoardFlowFlowing, flow.State)
}

// A quiet board is a legitimate resting state however old its last marker is:
// nothing was asked of the machine, so nothing is owed (criterion 6).
func TestAssessFlow_IdleWithNothingInFlight(t *testing.T) {
	flow := AssessFlow([]daemon.BoardViewCard{
		flowCard("SC-1", "", 30*time.Hour),
		flowCard("SC-2", "", 30*time.Hour),
		flowCard("SC-3", "", 30*time.Hour),
	}, flowNow)

	assert.Equal(t, daemon.BoardFlowIdle, flow.State)
}

func TestAssessFlow_IdleOnAnEmptyBoard(t *testing.T) {
	flow := AssessFlow(nil, flowNow)

	assert.Equal(t, daemon.BoardFlowIdle, flow.State)
	assert.Empty(t, flow.Since)
	assert.Zero(t, flow.InFlight)
}

// The desktop's quick phase ships cards with no derived stage at all; it must
// never flash a stall on the way to the real board (criterion 6).
func TestAssessFlow_FreshStartWithUnderivedCardsIsIdle(t *testing.T) {
	var cards []daemon.BoardViewCard
	for _, key := range []string{"SC-1", "SC-2", "SC-3", "SC-4", "SC-5"} {
		cards = append(cards, flowCard(key, "", -1))
	}

	flow := AssessFlow(cards, flowNow)

	assert.Equal(t, daemon.BoardFlowIdle, flow.State)
	assert.Empty(t, flow.Keys)
}

func TestAssessFlow_JustStartedWorkIsFlowing(t *testing.T) {
	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "running", time.Minute)}, flowNow)

	assert.Equal(t, daemon.BoardFlowFlowing, flow.State)
}

func TestAssessFlow_AtTheThresholdIsStalled(t *testing.T) {
	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "running", FlowStallAfter)}, flowNow)

	assert.Equal(t, daemon.BoardFlowStalled, flow.State)
}

func TestAssessFlow_JustUnderTheThresholdIsFlowing(t *testing.T) {
	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "running", FlowStallAfter-time.Second)}, flowNow)

	assert.Equal(t, daemon.BoardFlowFlowing, flow.State)
}

func TestAssessFlow_QueuedCountsAsInFlight(t *testing.T) {
	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "queued", 5*time.Hour)}, flowNow)

	assert.Equal(t, daemon.BoardFlowStalled, flow.State)
	assert.Equal(t, 1, flow.InFlight)
}

// A card held by a sequencing answer started nothing and owes no advance until
// the ticket it waits for is done — the same carve-out AgentNamesForCard makes
// for the analogous liveness judgement. Without it a permanent, unclearable
// stall is asserted while the machine is behaving exactly as told (SC-3577).
func TestAssessFlow_WaitsForHoldIsIdleNotStalled(t *testing.T) {
	held := flowCard("SC-1", "queued", 14*time.Hour)
	held.WaitsFor = "SC-2"

	flow := AssessFlow([]daemon.BoardViewCard{held}, flowNow)

	assert.Equal(t, daemon.BoardFlowIdle, flow.State)
	assert.Zero(t, flow.InFlight)
}

// An outage card is work the machine took on and still owes an advance on (AD4).
func TestAssessFlow_OutageCountsAsInFlight(t *testing.T) {
	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "outage", 5*time.Hour)}, flowNow)

	assert.Equal(t, daemon.BoardFlowStalled, flow.State)
	assert.Equal(t, 1, flow.InFlight)
}

// A live retry loop posts fresh markers, so it genuinely IS motion — the
// incident this signal catches is the one where no marker lands at all (AD4).
func TestAssessFlow_OutageRetryLoopReadsAsFlowing(t *testing.T) {
	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "outage", 3*time.Minute)}, flowNow)

	assert.Equal(t, daemon.BoardFlowFlowing, flow.State)
}

// Where the board cannot see, it says so rather than asserting either state
// (criterion 4).
func TestAssessFlow_DegradedCardsMakeItUnknown(t *testing.T) {
	degraded := flowCard("SC-2", "", 3*time.Hour)
	degraded.Degraded = true

	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "running", 14*time.Hour), degraded}, flowNow)

	assert.Equal(t, daemon.BoardFlowUnknown, flow.State)
	assert.Equal(t, 1, flow.Unreadable)
}

func TestAssessFlow_DegradedCardOnAQuietBoardIsUnknown(t *testing.T) {
	degraded := flowCard("SC-1", "", -1)
	degraded.Degraded = true

	flow := AssessFlow([]daemon.BoardViewCard{degraded}, flowNow)

	assert.Equal(t, daemon.BoardFlowUnknown, flow.State)
	assert.Zero(t, flow.InFlight)
	assert.Equal(t, 1, flow.Unreadable)
}

// One failed comment read on a moving board must not flap the strip: recent
// progress is checked before unreadability is (AD7).
func TestAssessFlow_DegradedButRecentProgressIsFlowing(t *testing.T) {
	degraded := flowCard("SC-2", "", 3*time.Hour)
	degraded.Degraded = true

	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "done", 3*time.Minute), degraded}, flowNow)

	assert.Equal(t, daemon.BoardFlowFlowing, flow.State)
}

func TestAssessFlow_InFlightWithoutATimestampIsUnknown(t *testing.T) {
	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "running", -1)}, flowNow)

	assert.Equal(t, daemon.BoardFlowUnknown, flow.State)
	assert.Equal(t, 1, flow.Unreadable)
}

// A date the board cannot parse is a card it cannot read, never a card that last
// moved in year zero.
func TestAssessFlow_UnparseableTimestampIsUnreadableNotAncient(t *testing.T) {
	card := daemon.BoardViewCard{Key: "SC-1", State: "running", StageEnteredAt: "not-a-time"}

	flow := AssessFlow([]daemon.BoardViewCard{card}, flowNow)

	assert.Equal(t, daemon.BoardFlowUnknown, flow.State)
	assert.Empty(t, flow.Since)
}

func TestAssessFlow_KeysAreSampledAndCountIsTrue(t *testing.T) {
	var cards []daemon.BoardViewCard
	for _, key := range []string{"SC-1", "SC-2", "SC-3", "SC-4", "SC-5", "SC-6"} {
		cards = append(cards, flowCard(key, "running", 5*time.Hour))
	}

	flow := AssessFlow(cards, flowNow)

	assert.Equal(t, daemon.BoardFlowStalled, flow.State)
	assert.Equal(t, 6, flow.InFlight)
	assert.Len(t, flow.Keys, FlowKeySample)
}

// Clearing is automatic: one new marker and the claim goes, with nothing to
// remember to take it down (criterion 5).
func TestAssessFlow_ClearsTheInstantWorkAdvances(t *testing.T) {
	stalled := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "running", 14*time.Hour)}, flowNow)
	assert.Equal(t, daemon.BoardFlowStalled, stalled.State)

	moved := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "running", 0)}, flowNow)
	assert.Equal(t, daemon.BoardFlowFlowing, moved.State)
}

// The threshold is a var precisely so it can be pinned; this is the only test
// that reassigns it, so it restores the value and never runs in parallel.
func TestAssessFlow_ThresholdIsOverridable(t *testing.T) {
	original := FlowStallAfter
	t.Cleanup(func() { FlowStallAfter = original })
	FlowStallAfter = time.Minute

	flow := AssessFlow([]daemon.BoardViewCard{flowCard("SC-1", "running", 5*time.Minute)}, flowNow)

	assert.Equal(t, daemon.BoardFlowStalled, flow.State)
}
