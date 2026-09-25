package cmddaemon

import (
	"testing"

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
func TestAttachActivity_FailedCardKeepsThePhaseItReached(t *testing.T) {
	isolateState(t)
	writePhase(t, "SC-1", "verify")
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{{Key: "SC-1", State: "failed"}}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	assert.Equal(t, "verifying", view.Cards[0].Activity)
}

// AC2: a resolved run (triage concluded no fix needed, or planning found
// nothing to plan) also keeps the phase it last recorded.
func TestAttachActivity_ResolvedCardKeepsThePhaseItReached(t *testing.T) {
	isolateState(t)
	writePhase(t, "SC-1", "triage")
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{{Key: "SC-1", State: "resolved"}}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	assert.Equal(t, "reproducing", view.Cards[0].Activity)
}

// AC3: nothing is invented. A failed card with no recorded phase is unchanged.
func TestAttachActivity_FailedCardWithNoRecordedPhaseIsUnchanged(t *testing.T) {
	isolateState(t)
	view := &daemon.BoardView{Cards: []daemon.BoardViewCard{{Key: "SC-1", State: "failed"}}}

	attachActivity(t.Context(), nil, view, zerolog.Nop())

	assert.Equal(t, "", view.Cards[0].Activity)
	assert.Equal(t, "", view.Cards[0].ActivityAt)
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
