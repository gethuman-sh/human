package cmdfsm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/pipelinefsm"
)

// run executes `human fsm …` against the compiled-in document and decodes the
// answer. The document is real on purpose: a fixture machine would let these
// pass while the shipped one answered differently, which is the whole failure
// this command exists to prevent one level down.
func run(t *testing.T, args ...string) map[string]any {
	t.Helper()
	cmd := BuildFSMCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	require.NoError(t, cmd.Execute(), "human fsm %s: %s", strings.Join(args, " "), out.String())

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), "output is not JSON: %s", out.String())
	return got
}

func runErr(t *testing.T, args ...string) error {
	t.Helper()
	cmd := BuildFSMCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestStates_ListsTheWholeMachine(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	got := run(t, "states")

	assert.Equal(t, doc.Initial, got["initial"])
	assert.Len(t, got["states"], len(doc.States), "every declared state is listed")
}

func TestStates_StageFilterNarrowsToThatColumn(t *testing.T) {
	got := run(t, "states", "--stage", "verification")

	states, ok := got["states"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, states, "the verification stage has states")
	for _, s := range states {
		board := s.(map[string]any)["board"].(map[string]any)
		assert.Equal(t, "verification", board["stage"])
	}
}

// A state's answer must carry the inherited liveness rule, not an empty field
// and a pointer to a stage default the caller would have to resolve itself.
func TestShow_ResolvesTheInheritedRule(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)
	var inheriting string
	for _, s := range doc.States {
		if s.Inherits != "" {
			inheriting = s.Name
			break
		}
	}
	require.NotEmpty(t, inheriting, "the document has a state that inherits its rule")

	got := run(t, "show", inheriting)

	assert.NotEmpty(t, got["stale_when"], "%s: the inherited rule is resolved, not left blank", inheriting)
	assert.NotEmpty(t, got["if_nothing_happens"], "%s: same for what happens if nobody acts", inheriting)
}

func TestShow_UnknownStateNamesTheOnesThatExist(t *testing.T) {
	err := runErr(t, "show", "not-a-state")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such state")
}

// THE guard. An agent is shown every way out — that is how it knows who it is
// waiting for — but is handed a runnable command only for its own. Without this
// an agent that merely finished its work could post [human:deployed] and put the
// item in a state nothing drove it to.
func TestNext_OnlyTheAskersOwnWaysOutCarryACommand(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	for actor := range doc.Actors {
		for _, state := range doc.StateNames() {
			for _, w := range waysOut(doc, state, actor) {
				if w.Actor == actor {
					assert.True(t, w.Yours, "%s/%s: %s is this actor's own edge", actor, state, w.Event)
					continue
				}
				assert.False(t, w.Yours, "%s/%s: %s belongs to %s", actor, state, w.Event, w.Actor)
				assert.Empty(t, w.Command,
					"%s/%s: %s belongs to %s and must carry no way to take it",
					actor, state, w.Event, w.Actor)
			}
		}
	}
}

// The command handed to a caller must carry every field its marker requires.
// Telling a caller half the contract only moves the failure to post time.
//
// It deliberately does NOT assert the type is in marker.KnownTypes(): that list
// is the types with a VALIDATION CONTRACT, and several real markers
// (pr-review-passed, pr-review-started, pr-started) are open-ended — postable,
// carried by daemon constants, contract-free. Asserting against it here would
// fail on correct commands. That the document's markers are real is already
// guarded, against the right list, by the daemon's own conformance test.
func TestNext_EveryCommandIsCompleteAndPostable(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	checked := 0
	for actor := range doc.Actors {
		for _, state := range doc.StateNames() {
			for _, w := range waysOut(doc, state, actor) {
				if w.Command == "" {
					continue
				}
				checked++
				fields := strings.Fields(w.Command)
				require.GreaterOrEqual(t, len(fields), 5, "%s: %q", w.Event, w.Command)
				markerType := fields[4]
				for _, required := range marker.RequiredFields(markerType) {
					assert.Contains(t, w.Command, "--field "+required+"=",
						"%s: %q omits the %q field that %s requires", w.Event, w.Command, required, markerType)
				}
			}
		}
	}
	assert.Positive(t, checked, "some edges do carry commands, or this test proves nothing")
}

// An alternation names a different marker per stage; a static answer cannot pick
// one, and picking wrong would hand back a command for another stage.
func TestNext_NoCommandForAMarkerThatVariesByStage(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	for _, e := range doc.Events {
		if len(e.Markers()) > 1 {
			assert.Empty(t, commandFor(e), "%s records several markers; no single command is correct", e.Name)
		}
	}
}

func TestNext_RejectsAnActorTheMachineDoesNotDeclare(t *testing.T) {
	err := runErr(t, "next", "planning", "--actor", "nobody")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such actor")
}

func TestMarker_ReportsWhereItMovesAnItem(t *testing.T) {
	got := run(t, "marker", "deployed")

	assert.Equal(t, "[human:deployed]", got["marker"])
	assert.Equal(t, true, got["moves_an_item"])
	assert.NotEmpty(t, got["moves"])
	assert.Contains(t, got["required_fields"], "pr", "the caller is told what the marker requires")
}

// Either form of the name answers, because a caller says it the way it posts it
// and the document writes it the way it appears on a ticket.
func TestMarker_AcceptsEitherForm(t *testing.T) {
	assert.Equal(t, run(t, "marker", "deployed"), run(t, "marker", "[human:deployed]"))
}

// A dual-role marker must not be reported as merely decorative: it moves an item
// from some states and records content from others, and an agent told only the
// second half would think posting it is free.
func TestMarker_DualRoleReportsBothHalves(t *testing.T) {
	got := run(t, "marker", "plan")

	assert.Equal(t, true, got["records_content"])
	assert.Equal(t, true, got["moves_an_item"])
	assert.NotEmpty(t, got["dual_role"], "and says why it is both")
}

func TestMarker_UnknownIsAnErrorRatherThanAnEmptyAnswer(t *testing.T) {
	err := runErr(t, "marker", "not-a-marker")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not mention this marker")
}

func TestConstants_CarriesTheBudgetsTheProseRefersToByName(t *testing.T) {
	got := run(t, "constants")

	constants, ok := got["constants"].(map[string]any)
	require.True(t, ok)
	for _, name := range []string{"DefaultStageRetries", "StuckRunningGrace", "MaxSilenceReaps"} {
		assert.Contains(t, constants, name)
	}
}

// The placeholders must survive as typed. Go's JSON encoder escapes angle
// brackets by default, and a caller pastes what it is handed.
func TestCommandPlaceholdersAreNotHTMLEscaped(t *testing.T) {
	cmd := BuildFSMCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"next", "implementing"})
	require.NoError(t, cmd.Execute())

	assert.Contains(t, out.String(), "<KEY>")
	assert.NotContains(t, out.String(), "\\u003c", "the escaped form must not reach a caller")
}
