package pipelinefsm

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replayMachine is a small machine with one of each shape replay has to handle:
// an ordinary recorded move, a silent move nothing records, a marker that fits
// two edges, and a marker the document says records content rather than movement.
func replayMachine() Document {
	no := false
	return Document{
		Version: 1, Describes: "test", Item: "ticket", Initial: "filed",
		Actors: map[string]string{"user": "a person", "daemon": "the host"},
		States: []State{
			{Name: "filed"}, {Name: "working"}, {Name: "checking"}, {Name: "paused"},
			{Name: "done", Terminal: true}, {Name: "gave-up", Terminal: true},
			{Name: "unseen"},
		},
		Unclassified: Unclassified{Markers: []string{"note"}},
		Events: []Event{
			{Name: "start", Src: []string{"filed"}, Dst: "working", Actor: "user", Marker: "[human:started]"},
			// Silent: nothing on the ticket records the walk from working to checking.
			{Name: "walk", Src: []string{"working"}, Dst: "checking", Actor: "daemon"},
			{Name: "pass", Src: []string{"checking"}, Dst: "done", Actor: "daemon", Marker: "[human:verdict]"},
			{Name: "fail", Src: []string{"checking"}, Dst: "gave-up", Actor: "daemon", Marker: "[human:verdict]"},
			// Non-moving: leaves every state, moves nothing.
			{Name: "observe", Src: []string{"filed", "working"}, Dst: "unseen", Actor: "daemon", MovesItem: &no},
			// A recorded phase: the pipeline says what it is doing, the item stays.
			{Name: "phase", Src: []string{"working"}, Dst: "working", Actor: "skill", Marker: "[human:phase]", MovesItem: &no},
			{Name: "ask", Src: []string{"working"}, Dst: "paused", Actor: "daemon", Marker: "[human:asked]"},
			// The destination is computed from something the marker header does
			// not carry, so the declared dst is only a placeholder.
			{Name: "resume", Src: []string{"paused"}, Dst: "working", Actor: "user", Marker: "[human:resumed]", DstIsDerived: true},
		},
	}
}

func TestReplay_FollowsRecordedMoves(t *testing.T) {
	r := replayMachine().Replay([]string{"started"})

	assert.Equal(t, "working", r.State)
	assert.Empty(t, r.Refused)
	assert.Empty(t, r.Ambiguous)
}

func TestReplay_AcceptsBothMarkerSpellings(t *testing.T) {
	bare := replayMachine().Replay([]string{"started"})
	wrapped := replayMachine().Replay([]string{"[human:started]"})

	assert.Equal(t, bare.State, wrapped.State)
	assert.Empty(t, wrapped.Refused)
}

// A silent move is invisible on the ticket, so an item that has taken one is
// still reported where it was — and the states it might really be in are named.
func TestReplay_BlursOverSilentMoves(t *testing.T) {
	r := replayMachine().Replay([]string{"started"})

	assert.Equal(t, []string{"checking", "working"}, r.Blur)
}

// The marker after a silent move has to be accepted, or every history that
// crosses one would read as a defect.
func TestReplay_MarkerAfterASilentMoveIsAccepted(t *testing.T) {
	r := replayMachine().Replay([]string{"started", "verdict"})

	assert.Empty(t, r.Refused)
	assert.Len(t, r.Ambiguous, 1, "verdict fits both pass and fail")
	assert.Equal(t, []string{"done", "gave-up"}, r.Ambiguous[0].Options)
}

func TestReplay_RefusesAMarkerThatFitsNothing(t *testing.T) {
	r := replayMachine().Replay([]string{"verdict"})

	require.Len(t, r.Refused, 1)
	assert.Equal(t, "verdict", r.Refused[0].Marker)
	assert.Equal(t, "filed", r.Refused[0].State, "says where the item had got to, not just what was refused")
}

// The whole point of FirstRefusal: a lost thread refuses everything after it, so
// the count has to be of defects, not of consequences.
func TestReplay_OnlyTheFirstRefusalIsTheDefect(t *testing.T) {
	r := replayMachine().Replay([]string{"verdict", "verdict", "verdict"})

	assert.Len(t, r.Refused, 3)
	first, ok := r.FirstRefusal()
	require.True(t, ok)
	assert.Equal(t, "filed", first.State)
}

func TestReplay_CleanHistoryHasNoFirstRefusal(t *testing.T) {
	_, ok := replayMachine().Replay([]string{"started"}).FirstRefusal()

	assert.False(t, ok)
}

// A marker the document declares as recording content must not read as a defect.
func TestReplay_UnclassifiedMarkersMoveNothing(t *testing.T) {
	r := replayMachine().Replay([]string{"started", "note"})

	assert.Empty(t, r.Refused)
	assert.Equal(t, "working", r.State)
}

// An observability edge leaves every state without the item moving. Counting it
// would make every state silently reachable from every other.
func TestReplay_NonMovingEdgesAreNotSilentMoves(t *testing.T) {
	r := replayMachine().Replay([]string{"started"})

	assert.NotContains(t, r.Blur, "unseen")
}

func TestReplay_ReportsReachingATerminal(t *testing.T) {
	r := replayMachine().Replay([]string{"started", "verdict"})

	assert.True(t, r.Terminal)
}

// A phase marker is the pipeline reporting what it is doing without the item
// going anywhere. Refusing it would report a defect for the machine working as
// described; moving on it would invent a position.
func TestReplay_PhaseMarkersAreAcceptedAndMoveNothing(t *testing.T) {
	r := replayMachine().Replay([]string{"started", "phase"})

	assert.Empty(t, r.Refused)
	assert.Equal(t, "working", r.State)
}

// And the stage's own markers keep working afterwards, which is the whole point:
// a phase inside a stage must not cost the stage its exits.
func TestReplay_APhaseDoesNotConsumeTheStagesExits(t *testing.T) {
	r := replayMachine().Replay([]string{"started", "phase", "verdict"})

	assert.Empty(t, r.Refused)
}

// A computed destination is the document declining to say where the item went.
// Believing the placeholder would report a position nothing supports.
func TestReplay_DerivedDestinationIsNotAPosition(t *testing.T) {
	r := replayMachine().Replay([]string{"started", "asked", "resumed"})

	assert.True(t, r.DestinationUnknown())
	assert.Equal(t, DerivedDestination, r.State)
	assert.Empty(t, r.Refused, "a computed destination is not a defect")
}

// The next recorded move says where the item had been, even though the previous
// one did not — so a history the document let go of is re-pinned rather than
// lost. Without this, every marker after a computed destination reads as a
// defect and the real disagreements stay hidden behind it.
func TestReplay_TheNextMarkerRepinsADerivedDestination(t *testing.T) {
	// "started" leaves only `filed`, which is nowhere near where the placeholder
	// pointed — so accepting it proves the search widened.
	r := replayMachine().Replay([]string{"started", "asked", "resumed", "started"})

	assert.Empty(t, r.Refused)
	assert.Equal(t, "working", r.State)
	assert.False(t, r.DestinationUnknown(), "the history is pinned again")
}

// The widening is bounded by the marker vocabulary: one that no edge anywhere
// records is still a defect. Otherwise a single computed destination would
// excuse every marker that followed it, and the histories that most need
// checking are exactly the ones that pass through a decision.
func TestReplay_DerivedDestinationStillRefusesAnUnknownMove(t *testing.T) {
	r := replayMachine().Replay([]string{"started", "asked", "resumed", "unrecorded"})

	require.Len(t, r.Refused, 1)
	assert.Equal(t, DerivedDestination, r.Refused[0].State)
}

// baseline is the recorded disagreement between the shipped machine and the
// histories the pipeline actually wrote, sorted by what should be done about it.
type baseline struct {
	Undescribed struct {
		Kinds map[string]int `json:"kinds"`
	} `json:"undescribed"`
	Violations struct {
		Kinds map[string]violation `json:"kinds"`
	} `json:"violations"`
	Legacy struct {
		Kinds map[string]int `json:"kinds"`
	} `json:"legacy"`
	Unexplained struct {
		Kinds map[string]int `json:"kinds"`
		// RuledOut is keyed by the entry — or the comma-joined entries — a note is
		// about, so a note cannot outlive its subject unnoticed.
		RuledOut map[string]string `json:"ruled_out"`
	} `json:"unexplained"`
	Ambiguities struct {
		Kinds map[string]int `json:"kinds"`
	} `json:"ambiguities"`
}

// violation is a move the machine forbids and the pipeline made anyway.
type violation struct {
	Count int    `json:"count"`
	Why   string `json:"why"`
}

// refusals flattens the three refusal buckets. Which bucket an entry sits in
// says what to do about it; the total is what the corpus must reproduce.
func (b baseline) refusals() map[string]int {
	all := map[string]int{}
	for k, v := range b.Undescribed.Kinds {
		all[k] = v
	}
	for k, v := range b.Unexplained.Kinds {
		all[k] = v
	}
	for k, v := range b.Legacy.Kinds {
		all[k] = v
	}
	for k, v := range b.Violations.Kinds {
		all[k] = v.Count
	}
	return all
}

func loadBaseline(t *testing.T) baseline {
	t.Helper()
	raw, err := os.ReadFile("testdata/replay-baseline.json")
	require.NoError(t, err)
	var b baseline
	require.NoError(t, json.Unmarshal(raw, &b))
	return b
}

// TestReplayCorpus_DisagreementMatchesTheBaseline is the differential check the
// document was missing: the checker proves the machine holds together, and this
// proves it describes what the pipeline does — against threads the pipeline wrote
// itself.
//
// It compares in BOTH directions on purpose. A new disagreement means the code
// moved away from the description. A baseline entry that no longer occurs means
// the description caught up and the worklist is stale, which has to be as loud —
// an allowlist nobody prunes stops being evidence and becomes permission.
func TestReplayCorpus_DisagreementMatchesTheBaseline(t *testing.T) {
	doc, err := Load()
	require.NoError(t, err)
	traces, err := LoadCorpus("testdata/replay-corpus.json")
	require.NoError(t, err)
	require.NotEmpty(t, traces)

	refusals, ambiguities := map[string]int{}, map[string]int{}
	for _, trace := range traces {
		r := doc.Replay(trace.Markers)
		if d, ok := r.FirstRefusal(); ok {
			refusals[d.Marker+"@"+d.State]++
		}
		for _, a := range r.Ambiguous {
			ambiguities[a.Marker+"@"+a.State]++
		}
	}

	want := loadBaseline(t)
	assert.Equal(t, want.refusals(), refusals,
		"the machine and the recorded histories disagree differently than the baseline records — "+
			"fix internal/pipelinefsm/pipeline-fsm.json, or update testdata/replay-baseline.json if the change is intended")
	assert.Equal(t, want.Ambiguities.Kinds, ambiguities,
		"the markers that fit more than one edge have changed")
}

// The buckets say what to do about a disagreement, so one entry sitting in two
// of them is two contradictory instructions — and it would go unnoticed, because
// flattening them for the count silently keeps whichever was merged last.
func TestReplayBaseline_BucketsAreDisjoint(t *testing.T) {
	b := loadBaseline(t)

	seen := map[string]string{}
	for bucket, kinds := range map[string][]string{
		"undescribed": keysOf(b.Undescribed.Kinds),
		"unexplained": keysOf(b.Unexplained.Kinds),
		"legacy":      keysOf(b.Legacy.Kinds),
		"violations":  keysOf(b.Violations.Kinds),
	} {
		for _, k := range kinds {
			if other, dup := seen[k]; dup {
				t.Errorf("%s is in both %s and %s — it can only mean one thing", k, other, bucket)
			}
			seen[k] = bucket
		}
	}
}

// An unexplained entry's note records what was ruled out, so the next reader
// starts where the last one stopped. A note whose subject has been explained and
// moved is worse than no note: it reads as current thinking about a live
// question that is already closed.
func TestReplayBaseline_RuledOutNotesNameLiveEntries(t *testing.T) {
	b := loadBaseline(t)

	for subject := range b.Unexplained.RuledOut {
		for _, kind := range strings.Split(subject, ", ") {
			_, live := b.Unexplained.Kinds[kind]
			assert.True(t, live,
				"a ruled-out note is written about %s, which is no longer unexplained — "+
					"fold what it says into the bucket the entry moved to, or delete it", kind)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The violation guard below is only worth anything if Accepts can say yes, so
// the shipped machine is asked one of each. Without this, an Accepts that always
// returned false would make that guard pass forever while checking nothing.
func TestDocumentAccepts_GivesBothAnswers(t *testing.T) {
	doc, err := Load()
	require.NoError(t, err)

	assert.True(t, doc.Accepts("planning", "plan-ready"), "a planner attaching its plan is a described move")
	assert.False(t, doc.Accepts("filed", "deployed"), "merging a Backlog ticket is not")
}

// TestReplayCorpus_NoViolationIsAbsorbedIntoTheMachine is the guard on the
// baseline's own integrity.
//
// A violation is the pipeline doing something the machine forbids, and there is
// an easy way to make one stop failing: widen the document until it is allowed.
// That closes the entry and destroys the reason the document exists, so the
// escape is closed here — every violation must still be a move the machine
// refuses. An entry that has genuinely become legitimate has to be argued into
// `undescribed` by hand, where the change is visible in the diff.
func TestReplayCorpus_NoViolationIsAbsorbedIntoTheMachine(t *testing.T) {
	doc, err := Load()
	require.NoError(t, err)

	for kind, v := range loadBaseline(t).Violations.Kinds {
		marker, state, ok := strings.Cut(kind, "@")
		require.True(t, ok, "a violation is written marker@state: %q", kind)
		assert.False(t, doc.Accepts(state, marker),
			"%s is recorded as a violation (%s) but the machine now allows it — "+
				"if that move became legitimate, move the entry to `undescribed` and say why", kind, v.Why)
	}
}

// A corpus with nothing in it would let every assertion above pass vacuously, so
// the evidence itself is held to a size.
func TestReplayCorpus_IsRealEvidence(t *testing.T) {
	traces, err := LoadCorpus("testdata/replay-corpus.json")
	require.NoError(t, err)

	assert.GreaterOrEqual(t, len(traces), 50, "too few histories to be evidence of anything")
	for _, trace := range traces {
		assert.NotEmpty(t, trace.Key)
		assert.NotEmpty(t, trace.Markers, "a history with no markers proves nothing")
	}
}

func TestLoadCorpus_ReportsAMissingFile(t *testing.T) {
	_, err := LoadCorpus("testdata/nope.json")

	assert.Error(t, err)
}
