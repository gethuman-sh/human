package stats

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainerSamples_rollUpPerStage(t *testing.T) {
	store, err := NewStatsStore(":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	now := time.Now().UTC()
	exit := 137

	rows := []ContainerSample{
		{Timestamp: now.Add(-2 * time.Minute), Agent: "board-SC-1-implementation", Stage: "implementation", MemUsage: 100, MemLimit: 1000, CPUPercent: 50},
		{Timestamp: now.Add(-1 * time.Minute), Agent: "board-SC-1-implementation", Stage: "implementation", MemUsage: 900, MemLimit: 1000, CPUPercent: 150},
		{Timestamp: now, Agent: "board-SC-1-implementation", Stage: "implementation", Phase: PhaseExit, MemUsage: 950, MemLimit: 1000, ExitCode: &exit, OOMKilled: true, Reason: "died"},
		{Timestamp: now, Agent: "board-SC-2-implementation", Stage: "implementation", MemUsage: 300, MemLimit: 1000, CPUPercent: 100},
		{Timestamp: now, Agent: "board-SC-2-planning", Stage: "planning", MemUsage: 50, MemLimit: 1000, CPUPercent: 10},
	}
	for _, r := range rows {
		require.NoError(t, store.InsertContainerSample(ctx, r))
	}

	got, err := store.QueryContainerResources(ctx, now.Add(-time.Hour), now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, got, 2)

	impl := got[0]
	assert.Equal(t, "implementation", impl.Stage)
	assert.Equal(t, 2, impl.Runs, "runs count distinct agents, not samples")
	assert.Equal(t, 4, impl.Samples)
	assert.Equal(t, uint64(950), impl.PeakMemBytes)
	assert.Equal(t, uint64(1000), impl.MemLimitBytes)
	assert.Equal(t, 150.0, impl.PeakCPUPercent)
	assert.InDelta(t, 75.0, impl.AvgCPUPercent, 0.001)
	assert.Equal(t, 1, impl.OOMKills)

	assert.Equal(t, "planning", got[1].Stage)
	assert.Equal(t, 0, got[1].OOMKills)
}

func TestContainerSamples_pruneRemovesOldRows(t *testing.T) {
	store, err := NewStatsStore(":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	old := time.Now().UTC().Add(-(RetentionDays + 1) * 24 * time.Hour)
	require.NoError(t, store.InsertContainerSample(ctx, ContainerSample{Timestamp: old, Agent: "a", Stage: "planning"}))
	require.NoError(t, store.InsertContainerSample(ctx, ContainerSample{Agent: "b", Stage: "planning"}))

	deleted, err := store.Prune(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	got, err := store.QueryContainerResources(ctx, time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, 1, got[0].Runs)
}

func TestContainerSamples_defaultsPhaseAndTimestamp(t *testing.T) {
	store, err := NewStatsStore(":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	require.NoError(t, store.InsertContainerSample(ctx, ContainerSample{Agent: "a", Stage: "review"}))

	var phase string
	require.NoError(t, store.db.QueryRowContext(ctx, "SELECT phase FROM container_samples").Scan(&phase))
	assert.Equal(t, PhaseRun, phase)
}
