package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/audit"
	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/stats"
)

// writeAgentRun creates <home>/.human/agent-logs/<agent>/<id>/ with a launch.json
// (startedAt) and, when reason is non-empty, an outcome.json. Used to exercise
// readAgentRunStats's direct on-disk read.
func writeAgentRun(t *testing.T, home, agent, id string, startedAt time.Time, reason string) {
	t.Helper()
	dir := filepath.Join(home, ".human", "agent-logs", agent, id)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	launch := map[string]any{"id": id, "agent": agent, "started_at": startedAt.Format(time.RFC3339Nano)}
	lb, err := json.Marshal(launch)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "launch.json"), lb, 0o600))

	if reason != "" {
		ob, err := json.Marshal(map[string]any{"reason": reason})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "outcome.json"), ob, 0o600))
	}
}

// auditEvent builds a minimal audit envelope with a fixed time and outcome so
// per-day bucketing and the success/failure split can be asserted.
func auditEvent(ts time.Time, outcome audit.Outcome) audit.Event {
	return audit.Event{
		SpecVersion: audit.SpecVersion,
		ID:          "id-" + ts.Format("150405.000000000"),
		Type:        "sh.human.tracker.create",
		Time:        ts,
		Data: audit.Data{
			Operation: "create",
			Actor:     audit.Actor{TrackerKind: "jira"},
			Resource:  audit.Resource{Key: "KAN-1"},
			Outcome:   outcome,
		},
	}
}

func TestReadAgentRunStats_shape(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now().UTC()
	writeAgentRun(t, home, "coder", "a1", now.Add(-time.Hour), "completed")
	writeAgentRun(t, home, "coder", "a2", now.Add(-2*time.Hour), "failed")
	writeAgentRun(t, home, "coder", "a3", now.Add(-3*time.Hour), "reaped")
	// Still-running: launch present, no outcome → counts as failure (not success).
	writeAgentRun(t, home, "reviewer", "b1", now.Add(-30*time.Minute), "")
	// Out of range: older than the since bound → excluded.
	writeAgentRun(t, home, "coder", "old", now.Add(-48*time.Hour), "completed")

	got := readAgentRunStats(now.Add(-24 * time.Hour))
	assert.Equal(t, 4, got.Total, "four in-range runs")
	assert.Equal(t, 1, got.Success, "only the completed run is a success")
	assert.Equal(t, 3, got.Failure, "failed + reaped + no-outcome are failures")
}

func TestReadAgentRunStats_missingDir(t *testing.T) {
	home := t.TempDir() // no .human/agent-logs at all
	t.Setenv("HOME", home)

	got := readAgentRunStats(time.Now().UTC().Add(-24 * time.Hour))
	assert.Equal(t, StatsHeadline{}, got, "no tree ⇒ zero, no panic")
}

func TestBuildStatsOverview_range(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // isolates agent-logs and the (absent) Claude projects root

	now := time.Now().UTC()

	statsStore, err := stats.NewStatsStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = statsStore.Close() })
	ctx := context.Background()
	// Two ok tool calls and one error, all within 24h.
	require.NoError(t, statsStore.InsertEvent(ctx, "s1", "PostToolUse", "Bash", "/p", "", now.Add(-time.Hour)))
	require.NoError(t, statsStore.InsertEvent(ctx, "s1", "PostToolUse", "Read", "/p", "", now.Add(-2*time.Hour)))
	require.NoError(t, statsStore.InsertEvent(ctx, "s1", "PostToolUseFailure", "Bash", "/p", "timeout", now.Add(-3*time.Hour)))

	auditStore, err := audit.NewStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = auditStore.Close() })
	require.NoError(t, auditStore.Insert(ctx, auditEvent(now.Add(-time.Hour), audit.OutcomeSuccess)))
	require.NoError(t, auditStore.Insert(ctx, auditEvent(now.Add(-2*time.Hour), audit.OutcomeDenied)))
	require.NoError(t, auditStore.Insert(ctx, auditEvent(now.Add(-25*time.Hour), audit.OutcomeFailure))) // outside 24h

	netStore := NewNetworkEventStore()
	netStore.Emit("proxy", "forward", "api.anthropic.com")
	netStore.Emit("proxy", "block", "evil.example")

	writeAgentRun(t, home, "coder", "a1", now.Add(-time.Hour), "completed")
	writeAgentRun(t, home, "coder", "a2", now.Add(-2*time.Hour), "failed")

	srv := &Server{
		Logger:          zerolog.Nop(),
		DaemonStartedAt: now.Add(-90 * 24 * time.Hour),
		StatsStore:      statsStore,
		AuditStore:      auditStore,
		NetworkEvents:   netStore,
	}

	// 24h window: excludes the 25h-old audit failure.
	ov, err := srv.buildStatsOverview(ctx, RangeDay)
	require.NoError(t, err)

	assert.Equal(t, "24h", ov.Range)
	assert.Equal(t, StatsHeadline{Total: 3, Success: 2, Failure: 1}, ov.ToolCalls)
	assert.Len(t, ov.ToolsByTool, 2)

	assert.Equal(t, 1, ov.Audit.Success, "only the in-window success")
	assert.Equal(t, 1, ov.Audit.Failure, "the in-window denied; the 25h failure is excluded")
	assert.Equal(t, StatsHeadline{Total: 2, Success: 1, Failure: 1}, ov.AgentRuns)
	assert.Len(t, ov.NetworkDecisions, 2, "network snapshot present regardless of range")

	// 7d window: now includes the previously-excluded audit failure.
	ov7, err := srv.buildStatsOverview(ctx, RangeWeek)
	require.NoError(t, err)
	assert.Equal(t, "7d", ov7.Range)
	assert.Equal(t, 2, ov7.Audit.Failure, "denied + the now-in-range failure")
	assert.Len(t, ov7.NetworkDecisions, 2, "network is range-exempt")
}

func TestBuildStatsOverview_emptyStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // no agent-logs, no Claude projects

	started := time.Now().UTC().Add(-time.Minute)
	srv := &Server{Logger: zerolog.Nop(), DaemonStartedAt: started}

	ov, err := srv.buildStatsOverview(context.Background(), RangeMonth)
	require.NoError(t, err)

	assert.Equal(t, "30d", ov.Range)
	assert.Equal(t, StatsHeadline{}, ov.ToolCalls)
	assert.Equal(t, StatsHeadline{}, ov.Audit)
	assert.Equal(t, StatsHeadline{}, ov.AgentRuns)
	assert.Equal(t, TokensHeadline{}, ov.Tokens)
	assert.Empty(t, ov.ToolsByTool)
	assert.Empty(t, ov.AuditByDay)
	assert.Empty(t, ov.NetworkDecisions)
	assert.Equal(t, started, ov.DaemonStartedAt, "DaemonStartedAt is propagated")
}

// TestBuildStatsOverview_tokenScanCachedWithinTTL proves the expensive JSONL
// walk is cached daemon-side: two same-range overviews within the TTL trigger
// exactly one scan, and both carry its tokens. This is what lets repeated polls
// and re-renders read the cache instead of re-walking history.
func TestBuildStatsOverview_tokenScanCachedWithinTTL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var calls int32
	scan := claude.TokenScan{
		WindowFresh:     4200,
		WindowCacheRead: 1300,
		PerHour:         []claude.TokenHourBucket{{Bucket: "2026-03-20 11:00", Fresh: 4200, CacheRead: 1300}},
		ByModel:         []claude.ModelTokens{{Model: "opus 4.8", Fresh: 300, CacheRead: 30}},
	}
	srv := &Server{
		Logger:          zerolog.Nop(),
		DaemonStartedAt: time.Now().UTC().Add(-90 * 24 * time.Hour),
		TokenScanner: func(_, _, _ time.Time) (claude.TokenScan, error) {
			atomic.AddInt32(&calls, 1)
			return scan, nil
		},
	}

	ctx := context.Background()
	ov1, err := srv.buildStatsOverview(ctx, RangeDay)
	require.NoError(t, err)
	ov2, err := srv.buildStatsOverview(ctx, RangeDay)
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "same range within TTL scans once")
	for _, ov := range []StatsOverview{ov1, ov2} {
		assert.Equal(t, 4200, ov.Tokens.Fresh)
		assert.Equal(t, 1300, ov.Tokens.CacheRead)
		assert.Equal(t, scan.PerHour, ov.TokensPerHour)
		assert.Equal(t, scan.ByModel, ov.TokensByModel)
	}
}

// TestBuildStatsOverview_tokenScanCacheKeyedByRange proves the cache is keyed by
// range: a day overview and a week overview each get their own scan, so a
// range switch never serves the wrong window from cache.
func TestBuildStatsOverview_tokenScanCacheKeyedByRange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var calls int32
	srv := &Server{
		Logger:          zerolog.Nop(),
		DaemonStartedAt: time.Now().UTC().Add(-90 * 24 * time.Hour),
		TokenScanner: func(_, _, _ time.Time) (claude.TokenScan, error) {
			atomic.AddInt32(&calls, 1)
			return claude.TokenScan{}, nil
		},
	}

	ctx := context.Background()
	_, err := srv.buildStatsOverview(ctx, RangeDay)
	require.NoError(t, err)
	_, err = srv.buildStatsOverview(ctx, RangeWeek)
	require.NoError(t, err)

	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "distinct ranges scan separately")
}

func TestParseRangeArg(t *testing.T) {
	assert.Equal(t, RangeWeek, parseRangeArg([]string{"--range", "7d"}))
	assert.Equal(t, RangeMonth, parseRangeArg([]string{"--range=30d"}))
	assert.Equal(t, RangeDay, parseRangeArg([]string{"--range", "bogus"}), "unknown ⇒ 24h")
	assert.Equal(t, RangeDay, parseRangeArg(nil), "absent ⇒ 24h")
}

func TestHandleStatsOverview_parsesRange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := &Server{Logger: zerolog.Nop(), DaemonStartedAt: time.Now().UTC()}
	resp := captureHandlerResponse(t, func(c net.Conn) { srv.handleStatsOverview(c, []string{"--range", "7d"}) })

	var ov StatsOverview
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &ov))
	assert.Equal(t, "7d", ov.Range)
}
