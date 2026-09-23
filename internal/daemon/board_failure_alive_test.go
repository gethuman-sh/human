package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/tracker"
)

// shortExitWait makes the liveness wait decide in milliseconds rather than the
// production five minutes.
func shortExitWait(t *testing.T) {
	t.Helper()
	poll, wait := cleanupExitPoll, cleanupExitWait
	cleanupExitPoll, cleanupExitWait = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { cleanupExitPoll, cleanupExitWait = poll, wait })
}

// A Stop event from a run whose claude process is still running is not that
// run's ending: claude fires one per main-agent turn, and a board run has one
// per subagent it waits for. Acting on it consumed the run record, posted a
// *-failed marker over live work, and left the real exit unhandled (SC-5088).
func TestHandleBoardAgentExit_StopWhileClaudeRunsIsNotTheRunsEnding(t *testing.T) {
	withInstantBoardExitRecheck(t)
	shortExitWait(t)
	runs := NewRunRegistry()
	runID := runs.Register("board-SC-1-planning", "SC-1", BoardPlanning)
	c := &syncCommenter{}
	var relaunched, resets []BoardStage
	deps := FailureDeps{
		CommenterFor: func() (tracker.Commenter, error) { return c, nil },
		Reachable:    alwaysReachable,
		Retry:        retryPolicyFor(ExitRetryable, true, &relaunched, &resets),
		Alive:        func(context.Context, string) (bool, error) { return true, nil },
		DaemonID:     "d1",
		Logger:       zerolog.Nop(),
	}

	handleBoardAgentExit(context.Background(), runs, hookevents.Event{
		EventName: hookevents.EventStop, RunID: runID, AgentName: "board-SC-1-planning",
	}, deps)

	assert.Empty(t, c.added, "no marker may be posted for a run that is still working")
	assert.Empty(t, relaunched, "nothing may be relaunched over live work")
	assert.Equal(t, 1, runs.Len(), "the run record must stay for the real ending")

	// The real ending: claude is gone, so the same run id is claimed and the
	// stage's failure is recorded and retried exactly once.
	deps.Alive = func(context.Context, string) (bool, error) { return false, nil }
	handleBoardAgentExit(context.Background(), runs, hookevents.Event{
		EventName: hookevents.EventStop, RunID: runID, AgentName: "board-SC-1-planning",
	}, deps)

	assert.Equal(t, []BoardStage{BoardPlanning}, relaunched)
	assert.Zero(t, runs.Len(), "the real ending consumes the record")
	require.NotEmpty(t, c.added)
}

// A probe that cannot reach the agent is not evidence the run is alive: the
// exit is handled exactly as before the probe existed — a reaped container has
// nothing left to ask.
func TestHandleBoardAgentExit_UnreachableProbeStillHandlesTheExit(t *testing.T) {
	withInstantBoardExitRecheck(t)
	shortExitWait(t)
	runs := NewRunRegistry()
	runID := runs.Register("board-SC-1-planning", "SC-1", BoardPlanning)
	c := &syncCommenter{}
	var relaunched, resets []BoardStage
	deps := FailureDeps{
		CommenterFor: func() (tracker.Commenter, error) { return c, nil },
		Reachable:    alwaysReachable,
		Retry:        retryPolicyFor(ExitRetryable, true, &relaunched, &resets),
		Alive:        func(context.Context, string) (bool, error) { return false, assert.AnError },
		DaemonID:     "d1",
		Logger:       zerolog.Nop(),
	}

	handleBoardAgentExit(context.Background(), runs, hookevents.Event{
		EventName: hookevents.EventStopFailure, RunID: runID, AgentName: "board-SC-1-planning",
	}, deps)

	assert.Equal(t, []BoardStage{BoardPlanning}, relaunched)
	assert.Zero(t, runs.Len())
}
