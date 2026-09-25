package cmddaemon

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/daemon"
)

// writePhase records a run's own phase word directly — the "stage.<phase>"
// namespace attachActivity reads (internal/board/activity.go StagePrefix),
// distinct from writeStageReport's full stage exit record.
func writePhase(t *testing.T, pmKey, phase string) {
	t.Helper()
	store, err := agentstate.Open(agentstate.DefaultDBPath())
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	_, err = store.Set(t.Context(), "", pmKey, "stage."+phase, "{}",
		agentstate.FormatJSON, agentstate.Meta{Agent: "test"})
	require.NoError(t, err)
}

// A running card's recorded phase is read exactly as before AC2 widened the
// predicate.
func TestAttachActivity_RunningCardKeepsItsPhase(t *testing.T) {
	isolateState(t)
	writePhase(t, "SC-1", "verify")
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{{Key: "SC-1", State: "running"}}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	assert.Equal(t, "verifying", view.Cards[0].Activity)
	assert.NotEmpty(t, view.Cards[0].ActivityAt)
}

// AC2: a run that ended badly (failed) still keeps the phase it reached — the
// fact survives the failure, and the renderer alone gives it its past tense.
// The card carries the StageRunStartedAt boundary a real failed card always
// has (its stage's own "-started" marker); attachActivity has no unbounded
// fallback for an ended card, so a boundary-less test would no longer exercise
// this path (SC-3656 PR review finding, round 3).
func TestAttachActivity_FailedCardKeepsThePhaseItReached(t *testing.T) {
	isolateState(t)
	since := time.Now().Format(time.RFC3339Nano)
	writePhase(t, "SC-1", "verify")
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{{Key: "SC-1", State: "failed", StageRunStartedAt: since}}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	assert.Equal(t, "verifying", view.Cards[0].Activity)
}

// AC2: a resolved run (triage concluded no fix needed, or planning found
// nothing to plan) also keeps the phase it last recorded, given the boundary a
// real resolved card always carries.
func TestAttachActivity_ResolvedCardKeepsThePhaseItReached(t *testing.T) {
	isolateState(t)
	since := time.Now().Format(time.RFC3339Nano)
	writePhase(t, "SC-1", "triage")
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{{Key: "SC-1", State: "resolved", StageRunStartedAt: since}}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	assert.Equal(t, "reproducing", view.Cards[0].Activity)
}

// AC3: nothing is invented. A failed card with no recorded phase is unchanged.
func TestAttachActivity_FailedCardWithNoRecordedPhaseIsUnchanged(t *testing.T) {
	isolateState(t)
	since := time.Now().Format(time.RFC3339Nano)
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{{Key: "SC-1", State: "failed", StageRunStartedAt: since}}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	assert.Equal(t, "", view.Cards[0].Activity)
	assert.Equal(t, "", view.Cards[0].ActivityAt)
}

// The store is keyed on the ticket alone, so a phase an EARLIER, unrelated
// stage wrote (stage.pr-review, from a review round weeks ago) is still
// readable when a later stage (stage.fix) is reaped or crashes before writing
// anything of its own. Without StageRunStartedAt bounding the read, the failed
// card would borrow that leftover phase and assert it, past tense, as its own
// (SC-3656 PR review finding). card_test asserts the negative half — nothing
// is shown — and the positive half is TestAttachActivity_FailedCardKeepsThePhaseItReached
// covers a phase the run itself wrote.
func TestAttachActivity_FailedCardDoesNotBorrowAnEarlierStagesPhase(t *testing.T) {
	isolateState(t)
	writePhase(t, "SC-1", "pr-review") // an earlier, unrelated stage's leftover write
	since := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{
		{Key: "SC-1", State: "failed", StageRunStartedAt: since},
	}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	assert.Equal(t, "", view.Cards[0].Activity, "a stage that wrote nothing of its own must not claim an earlier stage's phase")
	assert.Equal(t, "", view.Cards[0].ActivityAt)
}

// An ended card with NO StageRunStartedAt — e.g. [human:needs-planning] maps a
// refused launch straight to (planning, failed) with no planning-started
// marker ever written — has no run boundary to read its OWN phase against.
// attachActivity must skip it rather than falling back to an unbounded read
// over the whole ticket-wide scope, which would surface whatever an earlier,
// unrelated stage (e.g. a prior autofix round's stage.fix, still within
// agentstate's 14d retention) left behind and assert it as this run's phase
// (SC-3656 PR review finding, round 3).
func TestAttachActivity_EndedCardWithNoBoundaryShowsNoPhase(t *testing.T) {
	isolateState(t)
	writePhase(t, "SC-1", "fix") // an earlier, unrelated run's leftover write
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{
		{Key: "SC-1", State: "failed"}, // StageRunStartedAt left empty, as a launch-refused card has it
	}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	assert.Equal(t, "", view.Cards[0].Activity, "an ended card with no run boundary must not borrow an unrelated run's leftover phase")
	assert.Equal(t, "", view.Cards[0].ActivityAt)
}

// The bound is a floor, not a blanket suppression: a phase the CURRENT run
// itself wrote, at or after StageRunStartedAt, is still shown — and an older
// leftover from a prior stage does not win over it even though it is also
// present in the same per-ticket scope.
func TestAttachActivity_FailedCardKeepsItsOwnPhaseOverAnEarlierLeftover(t *testing.T) {
	isolateState(t)
	writePhase(t, "SC-1", "pr-review") // earlier stage's leftover write
	since := time.Now().Format(time.RFC3339Nano)
	writePhase(t, "SC-1", "fix") // this run's own write, after the boundary
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{
		{Key: "SC-1", State: "failed", StageRunStartedAt: since},
	}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	assert.Equal(t, "writing the fix", view.Cards[0].Activity)
	assert.NotEmpty(t, view.Cards[0].ActivityAt)
}

// A card whose run has not ended and is not working — done, queued, or a
// paused outage — gets no phase: it has either no run to show one for, or
// (outage) has not ended, so AC2's "ended" case does not reach it.
func TestAttachActivity_StatesWithNoEndedRunGetNoPhase(t *testing.T) {
	isolateState(t)
	writePhase(t, "SC-1", "verify")
	writePhase(t, "SC-2", "verify")
	writePhase(t, "SC-3", "verify")
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{
		{Key: "SC-1", State: "done"},
		{Key: "SC-2", State: "queued"},
		{Key: "SC-3", State: "outage"},
	}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	for _, c := range view.Cards {
		assert.Equal(t, "", c.Activity, "card %s", c.Key)
	}
}
