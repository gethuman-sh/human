package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// The sentinel carries the whole observation out of the sweep and back, and
// the form an older daemon composed still reads as a silence reap.
func TestSilenceReap_ErrorTypeRoundTrips(t *testing.T) {
	r := SilenceReap{Idle: 18 * time.Minute, Budget: 3 * time.Minute, Outstanding: "inside Bash, model request open"}
	got, ok := parseSilenceReap(r.ErrorType())
	require.True(t, ok)
	assert.Equal(t, r, got)

	old, ok := parseSilenceReap(ReapSilenceErrorType + ":18m0s")
	require.True(t, ok, "an older daemon's idle-only sentinel is still a silence reap")
	assert.Equal(t, 18*time.Minute, old.Idle)
	assert.Zero(t, old.Budget)
	assert.Empty(t, old.Outstanding)

	_, ok = parseSilenceReap("")
	assert.False(t, ok, "a genuine StopFailure carries no sentinel")
	_, ok = parseSilenceReap(ReapSilenceErrorType + ":not-a-duration")
	assert.False(t, ok)
}

// A genuine death composes no sentinel, so the exit handler takes the charged path.
func TestReapReason_GenuineDeathHasNoErrorType(t *testing.T) {
	assert.Equal(t, "", ReapReason{}.ErrorType())
	assert.True(t, strings.HasPrefix(ReapReason{Silent: true, Idle: time.Minute}.ErrorType(), ReapSilenceErrorType+":"))
}

// What the agent had in flight is named for the record, "none" when nothing was.
func TestAgentProgress_OutstandingWork(t *testing.T) {
	assert.Equal(t, "none", AgentProgress{ModelRequest: ModelRequestNone}.OutstandingWork())
	assert.Equal(t, "model request unknown", AgentProgress{}.OutstandingWork(), "unknown is a real answer, and it bought the generous budget")
	p := AgentProgress{InsideTool: true, Tool: "Bash", Subagents: 2, ModelRequest: ModelRequestOpen}
	assert.Equal(t, "inside Bash, 2 subagent(s) dispatched, model request open", p.OutstandingWork())
}

func silenceExit(errorType string) hookevents.Event {
	return hookevents.Event{AgentName: "board-SC-1-implementation", ErrorType: errorType, EventName: "StopFailure"}
}

func silenceDeps(c *syncCommenter, relaunched *[]BoardStage) FailureDeps {
	retry := StageRetry{
		Outcome:  func(string, BoardStage) (StageExit, bool) { return "", false },
		Attempts: func(string, BoardStage) (int, error) { return 1, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { *relaunched = append(*relaunched, s); return true, nil },
	}
	return FailureDeps{CommenterFor: func() (tracker.Commenter, error) { return c, nil }, Reachable: alwaysReachable, Retry: retry, Logger: zerolog.Nop()}
}

// One silence reap records what it was judged on as fields, beside the prose
// line the count still reads.
func TestHandleBoardAgentExit_SilenceReapRecordsObservation(t *testing.T) {
	c := &syncCommenter{comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))}}
	var relaunched []BoardStage
	reap := SilenceReap{Idle: 31 * time.Minute, Budget: 30 * time.Minute, Outstanding: "model request unknown"}

	handleBoardAgentExit(context.Background(), nil, silenceExit(reap.ErrorType()), silenceDeps(c, &relaunched))

	assert.Equal(t, []BoardStage{BoardImplementation}, relaunched)
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	m, ok := marker.ParseBody(c.added[0])
	require.True(t, ok)
	assert.Equal(t, MarkerImplementationFailed, m.Type)
	assert.Equal(t, "31m0s", m.Fields["idle"])
	assert.Equal(t, "30m0s", m.Fields["budget"])
	assert.Equal(t, "model request unknown", m.Fields["outstanding"])
	assert.Contains(t, m.Fields["reason"], silenceReapSentinel)
	assert.Empty(t, m.Fields["kind"], "a single reap is not a blocker")
	assert.NoError(t, marker.Validate(m))
}

// After the cap the give-up marker is a blocker record: every earlier stop
// quoted from its own marker, the stop that spent the cap last, the relaunch
// count, and a release condition — and the stage is not relaunched.
func TestHandleBoardAgentExit_SilenceReapGiveUpCarriesBlockerRecord(t *testing.T) {
	comments := []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))}
	earlier := []SilenceReap{
		{Idle: 4 * time.Minute, Budget: 3 * time.Minute, Outstanding: "none"},
		{Idle: 32 * time.Minute, Budget: 30 * time.Minute, Outstanding: "1 subagent(s) dispatched, model request unknown"},
		{Idle: 5 * time.Minute, Budget: 3 * time.Minute, Outstanding: "none"},
	}
	for i, r := range earlier {
		comments = append(comments,
			cmt(markerBody(silenceReapMarker(MarkerImplementationFailed, r), silenceReapFieldOrder...), time.Unix(int64(10+2*i), 0)),
			cmt(ImplementationStartedHeader, time.Unix(int64(11+2*i), 0)))
	}
	c := &syncCommenter{comments: comments}
	var relaunched []BoardStage
	last := SilenceReap{Idle: 6 * time.Minute, Budget: 3 * time.Minute, Outstanding: "none"}

	handleBoardAgentExit(context.Background(), nil, silenceExit(last.ErrorType()), silenceDeps(c, &relaunched))

	assert.Empty(t, relaunched, "the cap is spent")
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	m, ok := marker.ParseBody(c.added[0])
	require.True(t, ok)
	assert.NoError(t, marker.Validate(m), "the give-up is posted under the blocker contract the marker protocol enforces")
	assert.Equal(t, "other", m.Fields["kind"])
	assert.Contains(t, m.Fields["reason"], silenceReapGiveUpSentinel)
	assert.Equal(t, "3 automatic relaunches after silence stops, none charged against the retry budget", m.Fields["attempted"])
	assert.Contains(t, m.Fields["release"], "retries the implementation stage")
	lines := strings.Split(m.Fields["evidence"], "\n")
	require.Len(t, lines, 4, "three earlier stops and the one that spent the cap: %q", m.Fields["evidence"])
	assert.Equal(t, "stop 1: silent 4m0s, budget 3m0s, outstanding work: none", lines[0])
	assert.Equal(t, "stop 2: silent 32m0s, budget 30m0s, outstanding work: 1 subagent(s) dispatched, model request unknown", lines[1])
	assert.Equal(t, "stop 3: silent 5m0s, budget 3m0s, outstanding work: none", lines[2])
	assert.Equal(t, "stop 4 (this one): silent 6m0s, budget 3m0s, outstanding work: none", lines[3])
}

// A thread an older daemon wrote — reap markers with the prose line and no
// fields — still counts toward the cap and gives up, saying those stops were
// not recorded rather than inventing figures for them.
func TestHandleBoardAgentExit_SilenceReapGiveUpToleratesOldFormat(t *testing.T) {
	comments := []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))}
	for i := 0; i < MaxSilenceReaps; i++ {
		comments = append(comments,
			cmt(ImplementationFailedHeader+"\n"+silenceReapReason("5m0s"), time.Unix(int64(10+2*i), 0)),
			cmt(ImplementationStartedHeader, time.Unix(int64(11+2*i), 0)))
	}
	c := &syncCommenter{comments: comments}
	var relaunched []BoardStage

	handleBoardAgentExit(context.Background(), nil, silenceExit(ReapSilenceErrorType+":18m0s"), silenceDeps(c, &relaunched))

	assert.Empty(t, relaunched)
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.added, 1)
	m, ok := marker.ParseBody(c.added[0])
	require.True(t, ok)
	assert.Equal(t, "other", m.Fields["kind"])
	lines := strings.Split(m.Fields["evidence"], "\n")
	require.Len(t, lines, 4)
	assert.Equal(t, "stop 1: recorded by an older daemon without its budget or outstanding work", lines[0])
	assert.Equal(t, "stop 4 (this one): silent 18m0s, budget not recorded, outstanding work: not recorded", lines[3])
}

// The durable twin composes the same two markers from the same record.
func TestStuckRunningSilenceBody_RecordsObservationAndBlocker(t *testing.T) {
	reap := SilenceReap{Idle: 4 * time.Minute, Budget: 3 * time.Minute, Outstanding: "none"}
	comments := []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))}

	m, givingUp, skip := stuckRunningSilenceBody(MarkerImplementationFailed, BoardImplementation, comments, true, reap)
	require.False(t, skip)
	assert.False(t, givingUp)
	assert.Equal(t, "4m0s", m.Fields["idle"])
	assert.Equal(t, "3m0s", m.Fields["budget"])
	assert.Equal(t, "none", m.Fields["outstanding"])

	for i := 0; i < MaxSilenceReaps; i++ {
		comments = append(comments,
			cmt(markerBody(silenceReapMarker(MarkerImplementationFailed, reap), silenceReapFieldOrder...), time.Unix(int64(10+2*i), 0)),
			cmt(ImplementationStartedHeader, time.Unix(int64(11+2*i), 0)))
	}
	m, givingUp, skip = stuckRunningSilenceBody(MarkerImplementationFailed, BoardImplementation, comments, true, reap)
	require.False(t, skip)
	assert.True(t, givingUp)
	assert.Equal(t, "other", m.Fields["kind"])
	assert.Len(t, strings.Split(m.Fields["evidence"], "\n"), MaxSilenceReaps+1)
	assert.NotEmpty(t, m.Fields["release"])

	// A card that is stuck but whose agent simply vanished is not a silence
	// stop and carries no record.
	m, givingUp, skip = stuckRunningSilenceBody(MarkerImplementationFailed, BoardImplementation, comments, false, SilenceReap{})
	require.False(t, skip)
	assert.False(t, givingUp)
	assert.Empty(t, m.Fields["idle"])
}

// The rendered give-up survives the round trip through the tracker comment:
// the multi-line evidence comes back as it was posted, so a later reader (or
// a later daemon's silenceReapGaveUp dedup) sees the same record.
func TestSilenceReapGiveUpMarker_RendersAndParses(t *testing.T) {
	m := silenceReapGiveUpMarker(MarkerPlanningFailed, BoardPlanning, MaxSilenceReaps+1, nil, SilenceReap{Idle: time.Minute})
	body := markerBody(m, silenceReapFieldOrder...)
	back, ok := marker.ParseBody(body)
	require.True(t, ok)
	assert.Equal(t, m.Fields, back.Fields)
	stage, state, ok := ClassifyMarker(body)
	require.True(t, ok)
	assert.Equal(t, BoardPlanning, stage)
	assert.Equal(t, BoardFailed, state)
	assert.True(t, silenceReapGaveUp([]tracker.Comment{cmt(body, time.Unix(1, 0))}, BoardPlanning))
}
