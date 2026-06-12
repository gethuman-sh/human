package gui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/claude/logparser"
	"github.com/gethuman-sh/human/internal/claude/monitor"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
)

func TestToSnapshotDTO_Nil(t *testing.T) {
	dto := ToSnapshotDTO(nil, "host")
	assert.Empty(t, dto.Hostname)
	assert.False(t, dto.Daemon.Alive)
}

func TestToSnapshotDTO_FullMapping(t *testing.T) {
	fetched := time.Date(2026, 6, 12, 10, 30, 0, 0, time.UTC)
	started := fetched.Add(-time.Hour)
	completed := fetched.Add(-time.Minute)

	snap := &monitor.Snapshot{
		FetchedAt: fetched,
		Err:       errors.WithDetails("discovery failed"),
		Daemon: monitor.DaemonState{
			PID: 42, Alive: true, ProxyAddr: "127.0.0.1:19287", ProxyActiveConns: 3,
		},
		Telegram: "Telegram dispatch",
		Slack:    "Slack connected",
		Trackers: []tracker.TrackerStatus{
			{Name: "work", Kind: "linear", Label: "Linear", Working: true},
		},
		Instances: []monitor.InstanceView{
			{
				Usage: claude.InstanceUsage{
					Instance: claude.Instance{
						Label: "Host (PID 7046)", Source: "host", PID: 7046,
						Cwd: "/home/u/proj", ProxyConfigured: true, DaemonConnected: true,
						Memory: &claude.MemoryInfo{Usage: 100, Limit: 200},
					},
					Summary: &claude.UsageSummary{Models: map[string]*claude.ModelUsage{
						"claude-opus-4-6": {InputTokens: 10, OutputTokens: 20, CacheCreate: 1, CacheRead: 2},
						"claude-haiku":    {InputTokens: 1, OutputTokens: 2},
					}},
				},
				Session: &logparser.SessionState{
					SessionID: "sess-1", Slug: "magic-slug", Status: logparser.StatusWorking,
					StartedAt: started, LastActivity: fetched,
					CurrentTool: "Bash", BlockedTool: "Edit", ErrorType: "",
					Subagents: []logparser.Subagent{
						{Description: "explore", SubagentType: "Explore", StartedAt: started, CompletedAt: &completed, DurationMs: 1234},
					},
					Tasks: []logparser.Task{
						{Subject: "do thing", Status: "in_progress"},
					},
				},
			},
		},
		Panes: []claude.TmuxPane{
			{SessionName: "main", WindowIndex: 1, PaneIndex: 0, Cwd: "/home/u/proj", State: claude.StateBusy},
		},
		TotalUsage: &claude.UsageSummary{Models: map[string]*claude.ModelUsage{
			"claude-opus-4-6": {InputTokens: 11, OutputTokens: 22},
		}},
		NetworkEvents: []daemon.NetworkEvent{
			{Source: "proxy", Status: "forward", Host: "api.github.com", Count: 3},
		},
		ToolStats: &stats.ToolStats{TotalEvents: 99},
	}

	dto := ToSnapshotDTO(snap, "myhost")

	assert.Equal(t, "myhost", dto.Hostname)
	assert.Equal(t, "discovery failed", dto.Error)
	assert.Equal(t, 42, dto.Daemon.PID)
	assert.True(t, dto.Daemon.Alive)
	assert.EqualValues(t, 3, dto.Daemon.ProxyActiveConns)
	assert.Equal(t, "Telegram dispatch", dto.Telegram)
	assert.Equal(t, "Slack connected", dto.Slack)

	require.NotNil(t, dto.UsageWindow)
	assert.True(t, dto.UsageWindow.End.After(dto.UsageWindow.Start))

	require.Len(t, dto.Trackers, 1)
	assert.Equal(t, "linear", dto.Trackers[0].Kind)
	assert.True(t, dto.Trackers[0].Working)

	require.Len(t, dto.Instances, 1)
	inst := dto.Instances[0]
	assert.Equal(t, "Host (PID 7046)", inst.Label)
	assert.Equal(t, 7046, inst.PID)
	assert.EqualValues(t, 100, inst.MemoryUsage)
	assert.True(t, inst.DaemonConnected)

	// Models are sorted by name for stable wire output.
	require.Len(t, inst.Models, 2)
	assert.Equal(t, "claude-haiku", inst.Models[0].Model)
	assert.Equal(t, "claude-opus-4-6", inst.Models[1].Model)
	assert.Equal(t, 10, inst.Models[1].InputTokens)

	require.NotNil(t, inst.Session)
	assert.Equal(t, "working", inst.Session.Status)
	assert.Equal(t, "magic-slug", inst.Session.Slug)
	assert.Equal(t, "Bash", inst.Session.CurrentTool)
	require.Len(t, inst.Session.Subagents, 1)
	assert.Equal(t, "explore", inst.Session.Subagents[0].Description)
	assert.NotNil(t, inst.Session.Subagents[0].CompletedAt)
	require.Len(t, inst.Session.Tasks, 1)
	assert.Equal(t, "in_progress", inst.Session.Tasks[0].Status)

	require.Len(t, dto.Panes, 1)
	assert.Equal(t, "busy", dto.Panes[0].State)

	require.Len(t, dto.TotalUsage, 1)
	assert.Equal(t, 11, dto.TotalUsage[0].InputTokens)

	require.Len(t, dto.NetworkEvents, 1)
	assert.Equal(t, "api.github.com", dto.NetworkEvents[0].Host)
	require.NotNil(t, dto.ToolStats)
	assert.Equal(t, 99, dto.ToolStats.TotalEvents)
}

func TestSessionStatusString(t *testing.T) {
	cases := map[logparser.SessionStatus]string{
		logparser.StatusReady:   "ready",
		logparser.StatusWorking: "working",
		logparser.StatusBlocked: "blocked",
		logparser.StatusWaiting: "waiting",
		logparser.StatusError:   "error",
		logparser.StatusEnded:   "ended",
	}
	for status, want := range cases {
		assert.Equal(t, want, sessionStatusString(status))
	}
}

func TestInstanceStateString(t *testing.T) {
	cases := map[claude.InstanceState]string{
		claude.StateUnknown: "unknown",
		claude.StateBusy:    "busy",
		claude.StateReady:   "ready",
		claude.StateBlocked: "blocked",
		claude.StateWaiting: "waiting",
		claude.StateError:   "error",
	}
	for state, want := range cases {
		assert.Equal(t, want, instanceStateString(state))
	}
}
