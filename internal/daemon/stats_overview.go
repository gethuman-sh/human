package daemon

// Board stats aggregator. All five data sources the board's Stats view shows
// (tokens, tool calls, audit outcomes, agent runs, network decisions) live on
// the daemon host, and the desktop process talks only to the daemon — so a
// single daemon-side aggregator returns one payload and the range switch stays
// atomic across every panel. This reads only already-recorded local data; it
// adds no new collection and nothing leaves the machine.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gethuman-sh/human/internal/audit"
	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/stats"
)

// StatsRange is the time window the board's range switch selects.
type StatsRange string

const (
	// RangeDay is the trailing 24 hours.
	RangeDay StatsRange = "24h"
	// RangeWeek is the trailing 7 days.
	RangeWeek StatsRange = "7d"
	// RangeMonth is the trailing 30 days (the retention ceiling for both the
	// stats and audit stores).
	RangeMonth StatsRange = "30d"
)

// StatsHeadline is a total plus its success/failure split, used by the
// tool-call, audit and agent-run headline numbers.
type StatsHeadline struct {
	Total   int `json:"total"`
	Success int `json:"success"`
	Failure int `json:"failure"`
}

// TokensHeadline is the current-window token split. Tokens have no
// success/failure, so the headline shows fresh vs cache-read instead.
type TokensHeadline struct {
	Fresh     int `json:"fresh"`
	CacheRead int `json:"cacheRead"`
}

// AuditDayCount is one day's audit outcome breakdown for the audit panel.
type AuditDayCount struct {
	Day      string `json:"day"`      // "2006-01-02"
	Approved int    `json:"approved"` // outcome success
	Denied   int    `json:"denied"`   // outcome denied
	Failed   int    `json:"failed"`   // outcome failure
}

// StatsOverview is the consolidated board-stats payload for one range.
type StatsOverview struct {
	Range            string                   `json:"range"`
	GeneratedAt      time.Time                `json:"generatedAt"`
	DaemonStartedAt  time.Time                `json:"daemonStartedAt"`
	Tokens           TokensHeadline           `json:"tokens"`    // current 5h window
	ToolCalls        StatsHeadline            `json:"toolCalls"` // over range
	Audit            StatsHeadline            `json:"audit"`     // over range
	AgentRuns        StatsHeadline            `json:"agentRuns"` // over range
	TokensPerHour    []claude.TokenHourBucket `json:"tokensPerHour"`
	ToolsByTool      []stats.ToolCount        `json:"toolsByTool"`
	AuditByDay       []AuditDayCount          `json:"auditByDay"`
	NetworkDecisions []NetworkEvent           `json:"networkDecisions"` // live, ignores range
}

// rangeSince maps a StatsRange to the start of its window relative to now.
// An unknown range falls back to 24h, matching the route's default.
func rangeSince(r StatsRange, now time.Time) time.Time {
	switch r {
	case RangeWeek:
		return now.Add(-7 * 24 * time.Hour)
	case RangeMonth:
		return now.Add(-30 * 24 * time.Hour)
	default:
		return now.Add(-24 * time.Hour)
	}
}

// buildStatsOverview aggregates every board-stats source for the range. Each
// source is optional: an unset store or a missing on-disk tree yields an empty
// panel rather than an error, so a not-yet-configured feature reads as empty —
// matching the daemon's other read routes.
func (s *Server) buildStatsOverview(ctx context.Context, r StatsRange) (StatsOverview, error) {
	now := time.Now().UTC()
	since := rangeSince(r, now)

	ov := StatsOverview{
		Range:           string(r),
		GeneratedAt:     now,
		DaemonStartedAt: s.DaemonStartedAt,
	}

	// Tokens: current 5h window for the headline, per-hour buckets over the
	// range for the panel. The JSONL scan is best-effort — any error leaves the
	// token fields empty (the panel then shows its empty state) rather than
	// failing the whole overview.
	if root, err := claude.ClaudeProjectsRoot(); err == nil {
		if summary, err := claude.CalculateUsage(claude.OSDirWalker{}, root, now); err == nil {
			for _, mu := range summary.Models {
				if mu == nil {
					continue
				}
				ov.Tokens.Fresh += mu.InputTokens + mu.OutputTokens + mu.CacheCreate
				ov.Tokens.CacheRead += mu.CacheRead
			}
		}
		if buckets, err := claude.TokensByHour(claude.OSDirWalker{}, root, since, now); err == nil {
			ov.TokensPerHour = buckets
		}
	}

	// Tool calls: panel breakdown plus the ok/error headline split.
	if s.StatsStore != nil {
		byTool, err := s.StatsStore.QueryByTool(ctx, since, now)
		if err != nil {
			return StatsOverview{}, err
		}
		ov.ToolsByTool = byTool
		outcomes, err := s.StatsStore.QueryToolOutcomes(ctx, since, now)
		if err != nil {
			return StatsOverview{}, err
		}
		ov.ToolCalls = StatsHeadline{
			Total:   outcomes.OK + outcomes.Error,
			Success: outcomes.OK,
			Failure: outcomes.Error,
		}
	}

	// Audit: per-day approved/denied/failed for the panel, and a success/failure
	// headline where denied and failed both count as failure.
	if s.AuditStore != nil {
		byDay, headline, err := s.auditOverRange(ctx, since, now)
		if err != nil {
			return StatsOverview{}, err
		}
		ov.AuditByDay = byDay
		ov.Audit = headline
	}

	// Agent runs: read the agent-log tree directly (see readAgentRunStats) —
	// the daemon must not import internal/agent (that package imports daemon).
	ov.AgentRuns = readAgentRunStats(since)

	// Network decisions: a live in-memory snapshot with no historical
	// timestamps, so it is range-exempt by nature.
	if s.NetworkEvents != nil {
		ov.NetworkDecisions = s.NetworkEvents.Snapshot()
	}

	return ov, nil
}

// auditOverRange buckets audit events by UTC day and folds the per-day counts
// into a single success/failure headline. A large limit is used so a busy
// window is not silently truncated below the true counts.
func (s *Server) auditOverRange(ctx context.Context, since, until time.Time) ([]AuditDayCount, StatsHeadline, error) {
	events, err := s.AuditStore.Query(ctx, audit.Filter{Since: since, Until: until, Limit: 100000})
	if err != nil {
		return nil, StatsHeadline{}, err
	}

	byDay := make(map[string]*AuditDayCount)
	var headline StatsHeadline
	for _, e := range events {
		day := e.Time.UTC().Format("2006-01-02")
		d := byDay[day]
		if d == nil {
			d = &AuditDayCount{Day: day}
			byDay[day] = d
		}
		switch e.Data.Outcome {
		case audit.OutcomeSuccess:
			d.Approved++
			headline.Success++
		case audit.OutcomeDenied:
			d.Denied++
			headline.Failure++
		case audit.OutcomeFailure:
			d.Failed++
			headline.Failure++
		}
		headline.Total++
	}

	out := make([]AuditDayCount, 0, len(byDay))
	for _, d := range byDay {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, headline, nil
}

// agentLaunchShape and agentOutcomeShape mirror the fields readAgentRunStats
// needs from internal/agent's LaunchRecord / OutcomeRecord on-disk JSON. They
// are deliberately minimal local copies: the daemon cannot import internal/agent
// (it would form an import cycle), so a shape test guards these field tags
// against agentlog.go.
type agentLaunchShape struct {
	StartedAt time.Time `json:"started_at"`
}

type agentOutcomeShape struct {
	Reason string `json:"reason"` // "completed" | "failed" | "reaped"
}

// agentLogsRoot returns ~/.human/agent-logs, mirroring agent.ExecutionLogsDir's
// path without importing the package. Falls back to ./.human/agent-logs when the
// home directory is unknown, exactly as ExecutionLogsDir does.
func agentLogsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "agent-logs")
	}
	return filepath.Join(home, ".human", "agent-logs")
}

// readAgentRunStats counts agent runs started at or after since directly from
// the on-disk agent-log tree (<root>/<agent>/<id>/{launch.json,outcome.json}).
// A run with outcome reason "completed" is a success; a missing outcome (still
// running or crashed before writing) or reason failed/reaped is a failure. A
// missing tree means no agent ever ran and yields a zero headline.
func readAgentRunStats(since time.Time) StatsHeadline {
	var out StatsHeadline
	root := agentLogsRoot()

	agents, err := os.ReadDir(root)
	if err != nil {
		return out // never ran (or unreadable) — report zero
	}
	for _, agentDir := range agents {
		if !agentDir.IsDir() {
			continue
		}
		runsRoot := filepath.Join(root, agentDir.Name())
		runs, err := os.ReadDir(runsRoot)
		if err != nil {
			continue
		}
		for _, runDir := range runs {
			if !runDir.IsDir() {
				continue
			}
			dir := filepath.Join(runsRoot, runDir.Name())
			started, ok := readAgentLaunch(dir)
			if !ok || started.Before(since) {
				continue
			}
			out.Total++
			if agentRunSucceeded(dir) {
				out.Success++
			} else {
				out.Failure++
			}
		}
	}
	return out
}

// readAgentLaunch decodes launch.json's start time. ok is false when the file
// is missing or unparseable, so a half-written run dir is skipped entirely.
func readAgentLaunch(dir string) (time.Time, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "launch.json")) // #nosec G304 -- dir is under the agent-logs root
	if err != nil {
		return time.Time{}, false
	}
	var lr agentLaunchShape
	if err := json.Unmarshal(data, &lr); err != nil {
		return time.Time{}, false
	}
	return lr.StartedAt, true
}

// agentRunSucceeded reports whether the run's outcome.json records a completed
// reason. A missing or unparseable outcome counts as not-succeeded (the run
// died before recording one), matching how the agent log store treats it.
func agentRunSucceeded(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "outcome.json")) // #nosec G304 -- dir is under the agent-logs root
	if err != nil {
		return false
	}
	var or agentOutcomeShape
	if err := json.Unmarshal(data, &or); err != nil {
		return false
	}
	return or.Reason == "completed"
}
