package daemon

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/pipelinefsm"
	"github.com/gethuman-sh/human/internal/tracker"
)

// The machine describes where an item can BE. The derivation decides where a
// card actually IS. Nothing has ever compared the two, and they are written
// independently — so a place a card can sit that the machine does not describe
// would be invisible in exactly the way the pipeline's stuck-card bugs kept
// being invisible: not a wrong answer anywhere, just a question nobody asked.
//
// This asks it. It drives the real DeriveBoardCard over the real marker
// vocabulary and collects every (stage, state) placement that comes out, then
// checks each against the placements the states claim.
//
// It is a LOWER BOUND — an unclaimed placement found here is real, while the
// absence of one proves nothing. Every count this project has produced from real
// evidence has had the same shape.
//
// What that bound is NOT limited by any more is thread DEPTH. This used to
// enumerate pairs and disclaim placements needing three; measured, the set
// saturates at two — threes add none, and a one-off probe to four added none
// either. Threes are enumerated anyway, cheaply, so the depth the caveat used to
// worry about is covered by the guard rather than argued about in a comment.
// What still bounds it is marker SHAPE: each header is seeded with one body, and
// the options block with one stage-and-count, so a placement that needs a
// particular body cannot appear.

// producibleBoardPlacements returns every placement the derivation yields for
// some thread built out of the marker vocabulary.
//
// The vocabulary comes from orderedMarkerSpecs rather than a list written here:
// a list would be one more thing to keep in step with the markers, which is the
// drift this whole exercise exists to catch, reproduced inside the check for it.
func producibleBoardPlacements() map[Placement][]string {
	found := map[Placement][]string{}
	record := func(how string, comments []tracker.Comment, status tracker.Category, isIdea bool) {
		card := DeriveBoardCard(comments, status, isIdea)
		p := card.placement()
		if _, seen := found[p]; !seen {
			found[p] = []string{how}
		}
	}

	at := func(n int) time.Time { return time.Unix(int64(1000+n), 0) }
	body := func(header string) string { return header }

	record("no markers at all", nil, tracker.CategoryUnstarted, false)
	record("an idea-labelled ticket", nil, tracker.CategoryUnstarted, true)
	record("a closed ticket", []tracker.Comment{{Body: DeployedHeader, Created: at(0)}}, tracker.CategoryDone, false)

	// Every marker alone, then every ordered pair, then every ordered triple. The
	// pairs are what reach the override chain — a decision retiring a running
	// marker, a failure retired by a later start — which is where the synthesized
	// placements live. The triples reach nothing further today; they are here so
	// that a change which makes a third marker matter fails this test instead of
	// quietly widening what the enumeration cannot see.
	headers := make([]string, 0, len(orderedMarkerSpecs))
	for _, spec := range orderedMarkerSpecs {
		headers = append(headers, spec.Header)
	}
	// The two markers that carry meaning in their BODY, seeded per resumable
	// stage: an options block and the choice that consumes it are how a card
	// reaches the paused and queued placements at all.
	for stage := range optionStages {
		headers = append(headers,
			OptionsHeader+"\nstage: "+string(stage)+"\n1: one option",
			OptionChosenHeader+" 1: one option")
	}

	for _, first := range headers {
		record("["+placementHow(first)+"]", []tracker.Comment{{Body: body(first), Created: at(1)}}, tracker.CategoryUnstarted, false)
		for _, second := range headers {
			record(placementHow(first)+" then "+placementHow(second), []tracker.Comment{
				{Body: body(first), Created: at(1)},
				{Body: body(second), Created: at(2)},
			}, tracker.CategoryUnstarted, false)
			for _, third := range headers {
				record(placementHow(first)+" then "+placementHow(second)+" then "+placementHow(third),
					[]tracker.Comment{
						{Body: body(first), Created: at(1)},
						{Body: body(second), Created: at(2)},
						{Body: body(third), Created: at(3)},
					}, tracker.CategoryUnstarted, false)
			}
		}
	}
	return found
}

func placementHow(body string) string {
	line, _, _ := strings.Cut(body, "\n")
	return strings.TrimSpace(line)
}

// placementHow names a thread by its markers, so a finding says what puts a
// card there rather than only that something does.

// claimedBoardPlacements maps each placement the machine describes to the
// states claiming it. Several states may claim one placement — that is the blur
// a marker thread cannot resolve, not a contradiction.
func claimedBoardPlacements(t *testing.T) map[Placement][]string {
	t.Helper()
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	claimed := map[Placement][]string{}
	for _, s := range doc.States {
		if s.Board.Stage == "none" {
			continue
		}
		stages := []BoardStage{BoardStage(s.Board.Stage)}
		if s.Board.Stage == "any" {
			stages = []BoardStage{BoardBacklog, BoardPlanning, BoardImplementation, BoardVerification, BoardDoneStage}
		}
		for _, stage := range stages {
			p := placementOf(stage, BoardState(s.Board.State))
			claimed[p] = append(claimed[p], s.Name)
		}
	}
	return claimed
}

// TestBoardPlacement_EveryProducedPlacementIsLegal is the guard the Placement
// type exists for: whatever the derivation's rules compose, the result must be
// a placement the machine has a way to reach.
//
// It is the half the claimed-placement measurement below cannot cover. That one
// compares against pipeline-fsm.json, a document written by hand and widened by
// hand; this compares against the marker table the code itself dispatches on, so
// it fails on a pair no marker and no transition can produce — a card rendered
// somewhere the board cannot explain and no reconcile pass will move.
func TestBoardPlacement_EveryProducedPlacementIsLegal(t *testing.T) {
	for p, hows := range producibleBoardPlacements() {
		assert.True(t, p.legal(),
			"the derivation produces %s, which no marker or transition can reach — put %q through a named transition, or say why the pair is legal", p, hows[0])
	}
}

// A guard that calls everything legal is not a guard. These are pairs the two
// halves can spell but no rule produces: a closed card cannot be running, an
// idea cannot fail, and a stage with no agent to lose has no outage.
func TestBoardPlacement_APairNoRuleProducesIsIllegal(t *testing.T) {
	for _, p := range []Placement{
		placementOf(BoardHidden, BoardRunning),
		placementOf(BoardIdeas, BoardFailed),
		placementOf(BoardBacklog, BoardOutage),
		placementOf(BoardIdeas, BoardQueued),
	} {
		assert.False(t, p.legal(), "%s is a pair no rule produces and must not be legal", p)
	}
}

// The legal set is the derivation's own vocabulary, so every placement in it
// must be one the board can render. A pair that is legal but unreachable is the
// opposite drift: a rule that used to produce it and no longer does.
func TestBoardPlacement_TheLegalSetIsNamedByRealStagesAndStates(t *testing.T) {
	for _, p := range sortedLegalPlacements() {
		assert.NotEmpty(t, string(p.Stage()), "a legal placement must name a stage: %s", p)
		_, ranked := stageRank[p.Stage()]
		assert.True(t, ranked || p.Stage() == BoardHidden,
			"legal placement %s names a stage the board does not rank", p)
	}
}

// A placement the document names must be a placement that exists. This is the
// cheap half and it catches a typo or a stage renamed in the code and not here,
// which would otherwise make the expensive half below silently pass.
func TestBoardPlacement_TheMachineNamesRealPlacements(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	stages := map[string]bool{"any": true, "none": true}
	for _, s := range []BoardStage{BoardIdeas, BoardBacklog, BoardPlanning, BoardImplementation, BoardVerification, BoardDoneStage, BoardHidden, BoardTicketReview} {
		stages[string(s)] = true
	}
	states := map[string]bool{"none": true}
	for _, s := range []BoardState{BoardIdle, BoardRunning, BoardDone, BoardFailed, BoardResolved, BoardQueued, BoardOutage} {
		states[string(s)] = true
	}

	for _, s := range doc.States {
		assert.True(t, stages[s.Board.Stage], "%s: board stage %q is not a stage the derivation produces", s.Name, s.Board.Stage)
		assert.True(t, states[s.Board.State], "%s: board state %q is not a state the derivation produces", s.Name, s.Board.State)
	}
}

// TestBoardPlacement_EveryPlacementIsClaimed is the measurement.
//
// Recorded against a baseline rather than shipped failing, for the reason the
// replay baseline gives: a red test cannot be committed, and the finding is
// worth more written down than shouted. The baseline is compared in BOTH
// directions, so closing one of these must delete its row, and a new unclaimed
// placement fails immediately.
func TestBoardPlacement_EveryPlacementIsClaimed(t *testing.T) {
	claimed := claimedBoardPlacements(t)

	unclaimed := map[string]string{}
	for p, hows := range producibleBoardPlacements() {
		if len(claimed[p]) > 0 {
			continue
		}
		unclaimed[p.String()] = hows[0]
	}

	var recorded struct {
		Doc   string `json:"doc"`
		Bound string `json:"bound"`
		// Only the keys are compared. The values carry what puts a card there and
		// what is not known about it, which is the half a reader needs and no
		// assertion can check.
		Unclaimed map[string]json.RawMessage `json:"unclaimed"`
	}
	raw, err := os.ReadFile("testdata/board-placements.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &recorded))

	assert.Equal(t, keysOfMap(recorded.Unclaimed), keysOfMap(unclaimed),
		"the places a card can sit that the machine does not describe have changed — "+
			"describe the new one in internal/pipelinefsm/pipeline-fsm.json, or record it in "+
			"testdata/board-placements.json with what puts a card there")
}

func keysOfMap[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
