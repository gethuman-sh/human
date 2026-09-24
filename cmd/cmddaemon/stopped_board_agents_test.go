package cmddaemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/agent"
)

// The defect: a SIGKILLed/OOM-killed/crashed board agent is reaped within
// seconds by the zombie sweep, which writes StatusFailed to the meta and then
// deletes it outright (Manager.Delete = stopLocked + DeleteMeta) — so by the
// time stoppedBoardAgents runs, the meta this function used to read alone is
// already gone and the kill/OOM/crash case it exists for was invisible
// (SC-5327). PreserveExecutionArtifacts writes the same outcome to the
// execution log BEFORE DeleteMeta erases the meta, so that record survives.
func TestStoppedBoardAgents_ReapedExecutionSurvivesMetaDeletion(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	name := "board-sc-1-implementation"
	endedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	writeReapedExecution(t, name, endedAt)
	// No meta file at all: DeleteMeta already ran.

	stopped, err := stoppedBoardAgents()
	require.NoError(t, err)
	require.Contains(t, stopped, name)
	require.True(t, stopped[name].Equal(endedAt), "want %v, got %v", endedAt, stopped[name])
}

// A running agent's meta must never be reported stopped by either source,
// whatever an old execution log entry from a prior run says.
func TestStoppedBoardAgents_RunningAgentIsNeverReported(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	name := "board-sc-1-implementation"
	writeReapedExecution(t, name, time.Now().Add(-time.Hour))
	require.NoError(t, agent.WriteMeta(agent.Meta{Name: name, Status: agent.StatusRunning}))

	stopped, err := stoppedBoardAgents()
	require.NoError(t, err)
	require.NotContains(t, stopped, name)
}

// Manager.Refresh (`human agent list`) writes StatusStopped straight to the
// meta and never touches the execution log — the one producer the log does
// not capture, so the meta-based fallback must still surface it.
func TestStoppedBoardAgents_MetaOnlyStopIsStillReported(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	name := "board-sc-1-planning"
	stoppedAt := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	require.NoError(t, agent.WriteMeta(agent.Meta{Name: name, Status: agent.StatusStopped, StoppedAt: stoppedAt}))

	stopped, err := stoppedBoardAgents()
	require.NoError(t, err)
	require.True(t, stopped[name].Equal(stoppedAt))
}

// Only board agents are read from the execution log — an interactive agent's
// log is not worth the scan cost and must not surface here.
func TestStoppedBoardAgents_NonBoardExecutionIsSkipped(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	writeReapedExecution(t, "adhoc-session", time.Now().Add(-time.Minute))

	stopped, err := stoppedBoardAgents()
	require.NoError(t, err)
	require.NotContains(t, stopped, "adhoc-session")
}

// writeReapedExecution creates a minimal execution log entry for name whose
// newest (and only) run recorded a reaped disposition ending at endedAt,
// mirroring what PreserveExecutionArtifacts writes at the zombie sweep's
// remove choke point.
func writeReapedExecution(t *testing.T, name string, endedAt time.Time) {
	t.Helper()
	exe, err := agent.NewExecution(agent.LaunchRecord{ID: "exec1", Agent: name, StartedAt: endedAt.Add(-time.Hour)})
	require.NoError(t, err)
	require.NoError(t, exe.RecordDisposition(agent.DispositionReaped, endedAt, time.Hour))
}
