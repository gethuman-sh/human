package cmddaemon

import (
	"context"
	stderrors "errors"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/daemon"
)

// isolateState points the state store at a throwaway home so these tests never
// touch the developer's real ~/.human/state.db.
func isolateState(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func writeStageReport(t *testing.T, pmKey string, stage daemon.BoardStage, value string) {
	t.Helper()
	store, err := agentstate.Open(agentstate.DefaultDBPath())
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	_, err = store.Set(context.Background(), "", pmKey, stageReportName(stage), value,
		agentstate.FormatJSON, agentstate.Meta{Agent: "test"})
	require.NoError(t, err)
}

func TestStageExitClass_ReadsTheRecordedExit(t *testing.T) {
	isolateState(t)
	writeStageReport(t, "SC-1", daemon.BoardImplementation, `{"exit":"retryable","summary":"container died"}`)

	exit, found := stageExitClass(context.Background(), "", "SC-1", daemon.BoardImplementation, zerolog.Nop())

	require.True(t, found)
	require.Equal(t, daemon.ExitRetryable, exit)
}

// An agent that died before writing a report leaves nothing — the caller must
// be able to tell that apart from a recorded outcome. This exhausts the
// presence-settle backoff (SC-2378), so the backoff is shrunk to keep the test
// fast.
func TestStageExitClass_MissingReportIsNotFound(t *testing.T) {
	isolateState(t)
	shrinkPRLoopReadBackoff(t)

	exit, found := stageExitClass(context.Background(), "", "SC-1", daemon.BoardImplementation, zerolog.Nop())

	require.False(t, found)
	require.Empty(t, exit)
}

func TestStageExitClass_UnparseableReportIsNotFound(t *testing.T) {
	isolateState(t)
	shrinkPRLoopReadBackoff(t)
	store, err := agentstate.Open(agentstate.DefaultDBPath())
	require.NoError(t, err)
	_, err = store.Set(context.Background(), "", "SC-1", stageReportName(daemon.BoardImplementation),
		"not json at all", agentstate.FormatText, agentstate.Meta{})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	_, found := stageExitClass(context.Background(), "", "SC-1", daemon.BoardImplementation, zerolog.Nop())
	require.False(t, found)
}

// A report with no exit field is not an outcome the board can act on.
func TestStageExitClass_EmptyExitIsNotFound(t *testing.T) {
	isolateState(t)
	shrinkPRLoopReadBackoff(t)
	writeStageReport(t, "SC-1", daemon.BoardImplementation, `{"summary":"no exit here"}`)

	_, found := stageExitClass(context.Background(), "", "SC-1", daemon.BoardImplementation, zerolog.Nop())
	require.False(t, found)
}

// A report that lands just after the first (racy) read is picked up on the
// presence-settle re-read rather than being reported missing (SC-2378).
func TestStageExitClass_SettlesOnDelayedReport(t *testing.T) {
	isolateState(t)
	origStep, origTries := prLoopReadRecheckStep, prLoopReadRecheckTries
	prLoopReadRecheckStep = 2 * time.Millisecond
	prLoopReadRecheckTries = 10
	t.Cleanup(func() { prLoopReadRecheckStep, prLoopReadRecheckTries = origStep, origTries })

	written := make(chan struct{})
	go func() {
		time.Sleep(3 * prLoopReadRecheckStep)
		writeStageReport(t, "SC-1", daemon.BoardImplementation, `{"exit":"done"}`)
		close(written)
	}()

	exit, found := stageExitClass(context.Background(), "", "SC-1", daemon.BoardImplementation, zerolog.Nop())
	<-written

	require.True(t, found, "the presence-settle backoff must pick up the delayed report")
	require.Equal(t, "done", exit)
}

func TestBumpAndClearStageRetries(t *testing.T) {
	isolateState(t)
	ctx := context.Background()

	n, err := bumpStageRetries(ctx, "", "SC-1", daemon.BoardImplementation)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	n, err = bumpStageRetries(ctx, "", "SC-1", daemon.BoardImplementation)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	// A clean finish clears the count, so the next failure gets a full budget
	// instead of inheriting a spent one.
	clearStageRetries(ctx, "", "SC-1", daemon.BoardImplementation)

	n, err = bumpStageRetries(ctx, "", "SC-1", daemon.BoardImplementation)
	require.NoError(t, err)
	require.Equal(t, 1, n, "the count restarts after a clean finish")
}

// Counts are per stage: a flaky review must not consume the build's budget.
func TestBumpStageRetries_IsPerStage(t *testing.T) {
	isolateState(t)
	ctx := context.Background()

	_, err := bumpStageRetries(ctx, "", "SC-1", daemon.BoardImplementation)
	require.NoError(t, err)
	n, err := bumpStageRetries(ctx, "", "SC-1", daemon.BoardVerification)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestRetryCounterName_IsScopedToTheStage(t *testing.T) {
	require.Equal(t, "relaunch.implementation.attempts", retryCounterName(daemon.BoardImplementation))
	require.Equal(t, "stage.planning", stageReportName(daemon.BoardPlanning))
}

func TestTranslateLaunchErr(t *testing.T) {
	// The launcher bridges the agent single-flight sentinel to the daemon
	// AgentLauncher-contract sentinel so the board swallows a benign racing
	// retry; every other error (and nil) passes through unchanged (SC-1419).
	t.Run("nil passes through", func(t *testing.T) {
		require.NoError(t, translateLaunchErr(nil))
	})
	t.Run("bare sentinel maps to daemon sentinel", func(t *testing.T) {
		got := translateLaunchErr(agent.ErrAlreadyRunning)
		assert.True(t, stderrors.Is(got, daemon.ErrAgentAlreadyRunning))
	})
	t.Run("wrapped sentinel maps to daemon sentinel", func(t *testing.T) {
		wrapped := fmt.Errorf("start failed: %w", agent.ErrAlreadyRunning)
		got := translateLaunchErr(wrapped)
		assert.True(t, stderrors.Is(got, daemon.ErrAgentAlreadyRunning))
	})
	t.Run("unrelated error passes through", func(t *testing.T) {
		down := stderrors.New("docker down")
		got := translateLaunchErr(down)
		assert.Same(t, down, got)
		assert.False(t, stderrors.Is(got, daemon.ErrAgentAlreadyRunning))
	})
}
