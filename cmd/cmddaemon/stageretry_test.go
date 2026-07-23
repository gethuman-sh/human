package cmddaemon

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

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
	_, err = store.Set(context.Background(), pmKey, stageReportName(stage), value,
		agentstate.FormatJSON, agentstate.Meta{Agent: "test"})
	require.NoError(t, err)
}

func TestStageExitClass_ReadsTheRecordedExit(t *testing.T) {
	isolateState(t)
	writeStageReport(t, "SC-1", daemon.BoardImplementation, `{"exit":"retryable","summary":"container died"}`)

	exit, found := stageExitClass(context.Background(), "SC-1", daemon.BoardImplementation, zerolog.Nop())

	require.True(t, found)
	require.Equal(t, daemon.ExitRetryable, exit)
}

// An agent that died before writing a report leaves nothing — the caller must
// be able to tell that apart from a recorded outcome.
func TestStageExitClass_MissingReportIsNotFound(t *testing.T) {
	isolateState(t)

	exit, found := stageExitClass(context.Background(), "SC-1", daemon.BoardImplementation, zerolog.Nop())

	require.False(t, found)
	require.Empty(t, exit)
}

func TestStageExitClass_UnparseableReportIsNotFound(t *testing.T) {
	isolateState(t)
	store, err := agentstate.Open(agentstate.DefaultDBPath())
	require.NoError(t, err)
	_, err = store.Set(context.Background(), "SC-1", stageReportName(daemon.BoardImplementation),
		"not json at all", agentstate.FormatText, agentstate.Meta{})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	_, found := stageExitClass(context.Background(), "SC-1", daemon.BoardImplementation, zerolog.Nop())
	require.False(t, found)
}

// A report with no exit field is not an outcome the board can act on.
func TestStageExitClass_EmptyExitIsNotFound(t *testing.T) {
	isolateState(t)
	writeStageReport(t, "SC-1", daemon.BoardImplementation, `{"summary":"no exit here"}`)

	_, found := stageExitClass(context.Background(), "SC-1", daemon.BoardImplementation, zerolog.Nop())
	require.False(t, found)
}

func TestBumpAndClearStageRetries(t *testing.T) {
	isolateState(t)
	ctx := context.Background()

	n, err := bumpStageRetries(ctx, "SC-1", daemon.BoardImplementation)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	n, err = bumpStageRetries(ctx, "SC-1", daemon.BoardImplementation)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	// A clean finish clears the count, so the next failure gets a full budget
	// instead of inheriting a spent one.
	clearStageRetries(ctx, "SC-1", daemon.BoardImplementation)

	n, err = bumpStageRetries(ctx, "SC-1", daemon.BoardImplementation)
	require.NoError(t, err)
	require.Equal(t, 1, n, "the count restarts after a clean finish")
}

// Counts are per stage: a flaky review must not consume the build's budget.
func TestBumpStageRetries_IsPerStage(t *testing.T) {
	isolateState(t)
	ctx := context.Background()

	_, err := bumpStageRetries(ctx, "SC-1", daemon.BoardImplementation)
	require.NoError(t, err)
	n, err := bumpStageRetries(ctx, "SC-1", daemon.BoardVerification)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestRetryCounterName_IsScopedToTheStage(t *testing.T) {
	require.Equal(t, "relaunch.implementation.attempts", retryCounterName(daemon.BoardImplementation))
	require.Equal(t, "stage.planning", stageReportName(daemon.BoardPlanning))
}
