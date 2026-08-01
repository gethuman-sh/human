package stats

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

func newTestStore(t *testing.T) *StatsStore {
	t.Helper()
	s, err := NewStatsStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNewStatsStore_schemaCreation(t *testing.T) {
	s := newTestStore(t)
	// Verify the table exists by inserting a row.
	ctx := context.Background()
	err := s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: time.Now().UTC()})
	require.NoError(t, err)
}

// The activity columns added in SC-2461 must round-trip through persistence.
func TestInsertEvent_persistsNewColumns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{
		SessionID:    "s1",
		EventName:    "PostToolUse",
		ToolName:     "Bash",
		Cwd:          "/proj",
		Timestamp:    time.Now().UTC(),
		ToolInput:    `{"command":"go test ./..."}`,
		SubagentType: "reviewer",
		Model:        "opus-4",
		DurationMs:   1500,
	}))

	var toolInput, subagentType, model string
	var durationMs int64
	row := s.db.QueryRowContext(ctx,
		"SELECT tool_input, subagent_type, model, duration_ms FROM tool_events LIMIT 1")
	require.NoError(t, row.Scan(&toolInput, &subagentType, &model, &durationMs))
	assert.Equal(t, `{"command":"go test ./..."}`, toolInput)
	assert.Equal(t, "reviewer", subagentType)
	assert.Equal(t, "opus-4", model)
	assert.Equal(t, int64(1500), durationMs)
}

// An already-migrated DB reports duplicate-column on re-migration; that path
// must be a no-op so an old stats DB opens cleanly under a new binary.
func TestEnsureColumns_idempotent(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.ensureColumns())
	require.NoError(t, s.ensureColumns())
}

func TestInsertEvent_andQueryByTool(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: now}))
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: now}))
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Read", Cwd: "/proj", Timestamp: now}))

	since := now.Add(-1 * time.Hour)
	until := now.Add(1 * time.Hour)
	result, err := s.QueryByTool(ctx, since, until)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "Bash", result[0].ToolName)
	assert.Equal(t, 2, result[0].Count)
	assert.Equal(t, "Read", result[1].ToolName)
	assert.Equal(t, 1, result[1].Count)
}

func TestInsertEvent_nullableErrorType(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Insert with empty error_type (should be NULL).
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: now}))
	// Insert with non-empty error_type.
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUseFailure", ToolName: "Bash", Cwd: "/proj", ErrorType: "timeout", Timestamp: now}))

	result, err := s.QueryByEventName(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, result, 2)
}

func TestPrune_deletesOldEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-31 * 24 * time.Hour) // 31 days ago
	recent := time.Now().UTC()

	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: old}))
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Read", Cwd: "/proj", Timestamp: recent}))

	deleted, err := s.Prune(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	// Only the recent event should remain.
	total, err := s.QueryTotal(ctx, old.Add(-time.Hour), recent.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, total)
}

func TestPrune_keepsRecentEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	recent := time.Now().UTC().Add(-1 * 24 * time.Hour) // 1 day ago

	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: recent}))

	deleted, err := s.Prune(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
}

func TestQueryByHour(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	base := time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: base}))
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: base.Add(30 * time.Minute)}))
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Read", Cwd: "/proj", Timestamp: base.Add(90 * time.Minute)}))

	result, err := s.QueryByHour(ctx, base.Add(-time.Hour), base.Add(3*time.Hour))
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "2026-04-09 10:00", result[0].Bucket)
	assert.Equal(t, 2, result[0].Count)
	assert.Equal(t, "2026-04-09 11:00", result[1].Bucket)
	assert.Equal(t, 1, result[1].Count)
}

func TestQueryByEventName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: now}))
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Read", Cwd: "/proj", Timestamp: now}))
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUseFailure", ToolName: "Bash", Cwd: "/proj", ErrorType: "timeout", Timestamp: now}))

	result, err := s.QueryByEventName(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "PostToolUse", result[0].EventName)
	assert.Equal(t, 2, result[0].Count)
	assert.Equal(t, "PostToolUseFailure", result[1].EventName)
	assert.Equal(t, 1, result[1].Count)
}

func TestBuildToolStats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: now}))
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Read", Cwd: "/proj", Timestamp: now}))

	since := now.Add(-time.Hour)
	until := now.Add(time.Hour)
	stats, err := s.BuildToolStats(ctx, since, until)
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalEvents)
	assert.Len(t, stats.ByTool, 2)
	assert.Len(t, stats.ByEventName, 1)
}

func TestConcurrentWrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	const workers = 10
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range workers {
		go func(workerID int) {
			defer wg.Done()
			for range iterations {
				_ = s.InsertEvent(ctx, hookevents.Event{SessionID: fmt.Sprintf("s-%d", workerID), EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: now})
			}
		}(w)
	}
	wg.Wait()

	total, err := s.QueryTotal(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, workers*iterations, total)
}

func TestQueryByTool_emptyRange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	result, err := s.QueryByTool(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestQueryToolOutcomes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Two ok events (empty error_type → NULL) and one error event.
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: now}))
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Read", Cwd: "/proj", Timestamp: now}))
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUseFailure", ToolName: "Bash", Cwd: "/proj", ErrorType: "timeout", Timestamp: now}))

	got, err := s.QueryToolOutcomes(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, ToolOutcomeCounts{OK: 2, Error: 1}, got)
}

func TestQueryToolOutcomes_emptyRange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Event exists but outside the queried window → SUM over zero rows is NULL,
	// which must coalesce to zero rather than error.
	require.NoError(t, s.InsertEvent(ctx, hookevents.Event{SessionID: "s1", EventName: "PostToolUse", ToolName: "Bash", Cwd: "/proj", Timestamp: now.Add(-48 * time.Hour)}))

	got, err := s.QueryToolOutcomes(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, ToolOutcomeCounts{OK: 0, Error: 0}, got)
}
