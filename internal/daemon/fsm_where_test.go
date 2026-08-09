package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/pipelinefsm"
	"github.com/gethuman-sh/human/internal/tracker"
)

func whereDoc(t *testing.T) pipelinefsm.Document {
	t.Helper()
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)
	return doc
}

func whereOf(t *testing.T, comments []tracker.Comment, actor string) WhereReport {
	t.Helper()
	return BuildWhere(whereDoc(t), "SC-1", comments, tracker.CategoryUnstarted, false, actor,
		WhereDeps{Now: time.Unix(10_000, 0)})
}

// The ordinary case: one placement, one state, named outright.
func TestWhere_NamesTheStateWhenThePlacementIsUnambiguous(t *testing.T) {
	report := whereOf(t, []tracker.Comment{
		cmt(PlanReadyHeader, time.Unix(1000, 0)),
	}, "user")

	assert.Equal(t, "planned", report.State)
	assert.Empty(t, report.Why, "nothing had to be explained away")
	require.Len(t, report.Candidates, 1)
	assert.NotEmpty(t, report.Candidates[0].IfNothingHappens)
}

// THE guard, carried over from the command: an asker sees every way out, and
// only its own carry something it can run. Without it an agent that merely
// finished its work could post [human:deployed].
func TestWhere_OnlyTheAskersOwnWaysOutCarryACommand(t *testing.T) {
	doc := whereDoc(t)
	for actor := range doc.Actors {
		report := BuildWhere(doc, "SC-1", []tracker.Comment{
			cmt(ReadyForReviewHeader, time.Unix(1000, 0)),
		}, tracker.CategoryUnstarted, false, actor, WhereDeps{Now: time.Unix(10_000, 0)})

		require.NotEmpty(t, report.Candidates)
		for _, c := range report.Candidates {
			for _, w := range c.WaysOut {
				if w.Actor == actor {
					assert.True(t, w.Yours, "%s: %s is this actor's own edge", actor, w.Event)
					continue
				}
				assert.False(t, w.Yours)
				assert.Empty(t, w.Command,
					"%s: %s belongs to %s and must carry no way to take it", actor, w.Event, w.Actor)
			}
		}
	}
}

// implementation/running covers seven states. The newest marker in the stage
// names which phase reported, so the answer narrows rather than listing all
// seven.
func TestWhere_NarrowsABlurredPlacementByTheNewestMarker(t *testing.T) {
	report := whereOf(t, []tracker.Comment{
		cmt(ImplementationStartedHeader, time.Unix(1000, 0)),
		cmt(BugVerdictHeader+" confirmed", time.Unix(2000, 0)),
	}, "skill")

	assert.Equal(t, BoardImplementation, BoardStage(report.Board.Stage))
	assert.Equal(t, BoardRunning, BoardState(report.Board.State))
	assert.NotEmpty(t, report.Candidates)
	assert.Less(t, len(report.Candidates), 7, "the newest marker narrowed the seven phases")
}

// When it genuinely cannot tell, it says so and lists the candidates rather than
// picking one. Reporting the wrong state is worse than reporting several: an
// agent acting on a state it is not in takes an edge it does not own.
func TestWhere_ListsCandidatesWithAReasonRatherThanGuessing(t *testing.T) {
	report := whereOf(t, []tracker.Comment{
		cmt(ImplementationStartedHeader, time.Unix(1000, 0)),
	}, "skill")

	if report.State != "" {
		return // narrowed to one; nothing to explain
	}
	assert.Greater(t, len(report.Candidates), 1)
	assert.NotEmpty(t, report.Why, "an unresolved answer must say what stopped it resolving")
}

// A state any column can carry is matched on the state alone — an item does not
// leave its column to be stopped.
func TestWhere_ResolvesTheColumnIndependentStates(t *testing.T) {
	report := whereOf(t, []tracker.Comment{
		cmt(ImplementationStartedHeader, time.Unix(1000, 0)),
		cmt(ImplementationFailedHeader+"\nreason: x", time.Unix(2000, 0)),
	}, "user")

	assert.Equal(t, "stopped", report.State)
}

// A closed ticket has no state in the machine. It must say that plainly instead
// of naming one, which is the honest half of the gap the document still carries.
func TestWhere_SaysWhenNoStateDescribesThePlacement(t *testing.T) {
	report := BuildWhere(whereDoc(t), "SC-1",
		[]tracker.Comment{cmt(DeployedHeader, time.Unix(1000, 0))},
		tracker.CategoryDone, false, "skill", WhereDeps{Now: time.Unix(10_000, 0)})

	assert.Equal(t, string(BoardHidden), report.Board.Stage)
	assert.Empty(t, report.State)
	assert.Empty(t, report.Candidates)
	assert.Contains(t, report.Why, "no state in the machine describes")
}

// Liveness is read from the daemon's own record, never recomputed. A blocked
// agent is waiting for a person, not hung, and Stalled already holds that rule.
func TestWhere_ReportsLivenessFromTheDaemonsOwnRecord(t *testing.T) {
	now := time.Unix(10_000, 0)
	deps := WhereDeps{
		Now: now,
		Progress: func(string) (AgentProgress, bool) {
			return AgentProgress{LastEventAt: now.Add(-time.Hour), Blocked: true}, true
		},
	}
	report := BuildWhere(whereDoc(t), "SC-1",
		[]tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1000, 0))},
		tracker.CategoryUnstarted, false, "skill", deps)

	require.NotNil(t, report.Agent)
	assert.True(t, report.Agent.Known)
	assert.True(t, report.Agent.Blocked)
	assert.False(t, report.Agent.Stalled, "an agent waiting on a person is not hung")
}

// An unknown agent is reported as unknown rather than as dead: absent evidence
// and evidence of absence lead to opposite actions.
func TestWhere_DistinguishesNoRecordFromNotAlive(t *testing.T) {
	deps := WhereDeps{
		Now:      time.Unix(10_000, 0),
		Progress: func(string) (AgentProgress, bool) { return AgentProgress{}, false },
	}
	report := BuildWhere(whereDoc(t), "SC-1",
		[]tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1000, 0))},
		tracker.CategoryUnstarted, false, "skill", deps)

	require.NotNil(t, report.Agent)
	assert.False(t, report.Agent.Known)
}

// Asking where an item is must never spend the budget being asked about. The
// deps take a READER; StageRetry.Attempts increments and must never be wired
// here.
func TestWhere_ReadingTheBudgetDoesNotSpendIt(t *testing.T) {
	reads := 0
	deps := WhereDeps{
		Now:         time.Unix(10_000, 0),
		MaxAttempts: 2,
		Attempts:    func(string, BoardStage) (int, error) { reads++; return 1, nil },
	}
	comments := []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1000, 0))}

	first := BuildWhere(whereDoc(t), "SC-1", comments, tracker.CategoryUnstarted, false, "skill", deps)
	second := BuildWhere(whereDoc(t), "SC-1", comments, tracker.CategoryUnstarted, false, "skill", deps)

	require.NotNil(t, first.Budget)
	assert.Equal(t, 1, first.Budget.Spent)
	assert.Equal(t, 2, first.Budget.Of)
	assert.Equal(t, first.Budget.Spent, second.Budget.Spent, "asking twice reports the same, unspent, count")
	assert.Equal(t, 2, reads)
}

// A missing dependency drops its section rather than failing the answer: where
// an item is stays worth knowing when only the liveness record is unavailable.
func TestWhere_AnswersWithoutTheOptionalSections(t *testing.T) {
	report := whereOf(t, []tracker.Comment{cmt(PlanReadyHeader, time.Unix(1000, 0))}, "user")

	assert.Equal(t, "planned", report.State)
	assert.Nil(t, report.Agent)
	assert.Nil(t, report.Budget)
}

// A marker IS a comment, so when it was posted is when the item moved. That is
// the only record of pipeline timing anywhere in the tool — `human marker list`
// reports type and fields and no times at all.
func TestWhere_EnteredComesFromTheMarkerComment(t *testing.T) {
	now := time.Unix(10_000, 0)
	arrived := now.Add(-90 * time.Minute)
	report := BuildWhere(whereDoc(t), "SC-1", []tracker.Comment{
		cmt(PlanningStartedHeader, arrived.Add(-time.Hour)),
		cmt(PlanReadyHeader, arrived),
	}, tracker.CategoryUnstarted, false, "user", WhereDeps{Now: now})

	require.NotNil(t, report.Entered)
	assert.Equal(t, arrived.UTC().Format(time.RFC3339), report.Entered.At)
	assert.Equal(t, 5400, report.Entered.Seconds, "how long the ITEM has sat here, not how idle an agent is")
	assert.Equal(t, PlanReadyHeader, report.Entered.Via)
}

// History is where the item has BEEN, in states, and stops before where it is
// now — a trail whose last entry might or might not be "now" is the confusing
// shape this avoids.
func TestWhere_HistoryIsStatesAndStopsBeforeNow(t *testing.T) {
	now := time.Unix(10_000, 0)
	report := BuildWhere(whereDoc(t), "SC-1", []tracker.Comment{
		cmt(PlanningStartedHeader, now.Add(-3*time.Hour)),
		cmt(PlanReadyHeader, now.Add(-2*time.Hour)),
		cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
	}, tracker.CategoryUnstarted, false, "skill", WhereDeps{Now: now})

	require.Len(t, report.History, 2, "the newest marker is the current position, not history")
	assert.Equal(t, "planning", report.History[0].State, "oldest first")
	assert.Equal(t, "planned", report.History[1].State)
	assert.Equal(t, 10800, report.History[0].SecondsAgo)
	assert.Equal(t, ImplementationStartedHeader, report.Entered.Via,
		"and the newest one is reported as where it is now")
}

// A marker several transitions record cannot be turned into one state. The entry
// stays legible via its marker rather than carrying a plausible guess an agent
// would then reason from.
func TestWhere_HistoryLeavesAnAmbiguousStateBlank(t *testing.T) {
	now := time.Unix(10_000, 0)
	report := BuildWhere(whereDoc(t), "SC-1", []tracker.Comment{
		cmt(ImplementationStartedHeader, now.Add(-2*time.Hour)),
		cmt(ReadyForReviewHeader, now.Add(-time.Hour)),
	}, tracker.CategoryUnstarted, false, "skill", WhereDeps{Now: now})

	require.Len(t, report.History, 1)
	assert.Equal(t, ImplementationStartedHeader, report.History[0].Marker,
		"the marker is always there even when the state cannot be named")
	assert.Empty(t, report.History[0].State,
		"implementation-started leads to both preflight and implementing, so neither is claimed")
}

// Bounded, so a long-running ticket does not hand an agent its whole life story.
func TestWhere_HistoryIsBounded(t *testing.T) {
	now := time.Unix(100_000, 0)
	var comments []tracker.Comment
	for i := range 30 {
		comments = append(comments, cmt(PlanningStartedHeader, now.Add(-time.Duration(30-i)*time.Hour)))
	}
	report := BuildWhere(whereDoc(t), "SC-1", comments, tracker.CategoryUnstarted, false, "skill",
		WhereDeps{Now: now, HistoryLimit: 4})

	assert.Len(t, report.History, 4, "the most recent, not the first")
	assert.Equal(t, DefaultWhereHistory, 10, "and the default covers a full PR review→fix loop")
}

// A caller that only wants the position can say so.
func TestWhere_HistoryCanBeSuppressed(t *testing.T) {
	now := time.Unix(10_000, 0)
	report := BuildWhere(whereDoc(t), "SC-1", []tracker.Comment{
		cmt(PlanningStartedHeader, now.Add(-2*time.Hour)),
		cmt(PlanReadyHeader, now.Add(-time.Hour)),
	}, tracker.CategoryUnstarted, false, "user", WhereDeps{Now: now, HistoryLimit: -1})

	assert.Empty(t, report.History)
	assert.NotNil(t, report.Entered, "but where it is now still answers")
}

// No comment bodies ride along. A full thread invites an agent to re-derive its
// own view of where the item is and disagree with the machine.
func TestWhere_HistoryCarriesNoCommentBodies(t *testing.T) {
	now := time.Unix(10_000, 0)
	secret := PlanReadyHeader + "\nbody: something an agent might re-derive from"
	report := BuildWhere(whereDoc(t), "SC-1", []tracker.Comment{
		cmt(secret, now.Add(-2*time.Hour)),
		cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
	}, tracker.CategoryUnstarted, false, "skill", WhereDeps{Now: now})

	require.Len(t, report.History, 1)
	assert.Equal(t, PlanReadyHeader, report.History[0].Marker)
	assert.NotContains(t, report.History[0].Marker, "re-derive")
}
