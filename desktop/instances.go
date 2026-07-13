//go:build wailsapp

package main

import (
	"context"
	"os"

	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/claude/logparser"
	"github.com/gethuman-sh/human/internal/claude/monitor"
)

// ModelUsage is the frontend-facing per-model token tally for one instance. The
// frontend derives each bar's percentage from the sum of these, mirroring the
// TUI's renderModelBars grand-total logic rather than trusting a server-computed
// percentage that could drift from the displayed in/out figures.
type ModelUsage struct {
	Name         string `json:"name"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
}

// SubagentInfo is the frontend-facing shape of one spawned Agent tool_use. Done
// distinguishes a finished agent (✓) from a running one (spinner); StartedAtUnix
// lets the frontend tick the elapsed clock between polls, and DurationMs gives
// the final wall time once complete.
type SubagentInfo struct {
	Description   string `json:"description"`
	Type          string `json:"type"`
	Done          bool   `json:"done"`
	StartedAtUnix int64  `json:"startedAtUnix"`
	DurationMs    int64  `json:"durationMs"`
}

// AgentInstance is the flat, frontend-facing shape of one running Claude Code
// instance — the desktop equivalent of a TUI instance row. Only scalar fields
// are mapped; the discovery types carry a DirWalker interface and pointer memory
// info that do not round-trip through JSON.
type AgentInstance struct {
	Label           string         `json:"label"`
	Source          string         `json:"source"` // "host" or "container"
	Status          string         `json:"status"` // ready|working|blocked|waiting|error|ended, "" when no session
	HasActivity     bool           `json:"hasActivity"`
	Slug            string         `json:"slug,omitempty"`
	PID             int            `json:"pid"`
	ContainerID     string         `json:"containerID,omitempty"`
	Cwd             string         `json:"cwd,omitempty"`
	Memory          string         `json:"memory,omitempty"`
	CurrentTool     string         `json:"currentTool,omitempty"`
	BlockedTool     string         `json:"blockedTool,omitempty"`
	ErrorType       string         `json:"errorType,omitempty"`
	StartedAtUnix   int64          `json:"startedAtUnix"`
	DaemonConnected bool           `json:"daemonConnected"`
	ProxyConfigured bool           `json:"proxyConfigured"`
	Models          []ModelUsage   `json:"models"`
	TasksPending    int            `json:"tasksPending"`
	TasksInProgress int            `json:"tasksInProgress"`
	TasksDone       int            `json:"tasksDone"`
	Subagents       []SubagentInfo `json:"subagents"`
}

// InstancesData is the payload the Agents view renders: the instance list plus an
// optional discovery error surfaced as a banner.
type InstancesData struct {
	Agents []AgentInstance `json:"agents"`
	Error  string          `json:"error,omitempty"`
}

// Instances discovers the running Claude Code instances (host processes and
// containers) and maps them to the frontend shape. Unlike the board methods this
// does NOT go through the daemon: instance discovery needs no credentials, and
// running the monitor in-process reuses the exact code path the TUI renders (see
// cmd/cmdtui/tui.go), guaranteeing the desktop Agents view matches it. It cannot
// be served from the daemon anyway — internal/claude/monitor imports
// internal/daemon, so the daemon importing monitor would be a cycle.
func (a *App) Instances() (InstancesData, error) {
	finder, dc := buildInstanceFinder()
	snap := monitor.New(finder, dc).FetchFull(context.Background())

	data := InstancesData{Agents: []AgentInstance{}}
	if snap.Err != nil {
		data.Error = snap.Err.Error()
	}
	for _, iv := range snap.Instances {
		data.Agents = append(data.Agents, agentInstanceFromView(iv))
	}
	return data, nil
}

// buildInstanceFinder assembles the same host+docker finder the TUI uses
// (cmd/cmdtui/tui.go buildFinder). Docker discovery is added only when an engine
// is reachable; the returned client is nil otherwise, which monitor tolerates.
func buildInstanceFinder() (claude.InstanceFinder, claude.DockerClient) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	finders := []claude.InstanceFinder{
		&claude.HostFinder{Runner: claude.OSCommandRunner{}, HomeDir: home},
	}
	var dc claude.DockerClient
	if client, dcErr := claude.NewEngineDockerClient(); dcErr == nil {
		dc = client
		finders = append(finders, &claude.DockerFinder{Client: dc})
	}
	return &claude.CombinedFinder{Finders: finders}, dc
}

// agentInstanceFromView maps one monitor.InstanceView to the frontend shape.
func agentInstanceFromView(iv monitor.InstanceView) AgentInstance {
	inst := iv.Usage.Instance
	ai := AgentInstance{
		Label:           inst.Label,
		Source:          inst.Source,
		PID:             inst.PID,
		ContainerID:     inst.ContainerID,
		Cwd:             inst.Cwd,
		Memory:          claude.FormatMemory(inst.Memory),
		DaemonConnected: inst.DaemonConnected,
		ProxyConfigured: inst.ProxyConfigured,
		Models:          modelUsages(iv.Usage.Summary),
		Subagents:       []SubagentInfo{},
	}
	applySessionFields(&ai, iv.Session)
	return ai
}

// applySessionFields folds the parsed session (status, slug, timing, tasks,
// subagents) into the instance shape. A nil session leaves the zero values,
// which the frontend renders as an idle, session-less instance.
func applySessionFields(ai *AgentInstance, sess *logparser.SessionState) {
	if sess == nil {
		return
	}
	ai.Status = statusString(sess.Status)
	ai.HasActivity = !sess.LastActivity.IsZero()
	ai.Slug = sess.Slug
	ai.CurrentTool = sess.CurrentTool
	ai.BlockedTool = sess.BlockedTool
	ai.ErrorType = sess.ErrorType
	if !sess.StartedAt.IsZero() {
		ai.StartedAtUnix = sess.StartedAt.Unix()
	}
	for _, t := range sess.Tasks {
		switch t.Status {
		case "completed":
			ai.TasksDone++
		case "in_progress":
			ai.TasksInProgress++
		default:
			ai.TasksPending++
		}
	}
	for _, sa := range sess.Subagents {
		si := SubagentInfo{
			Description: sa.Description,
			Type:        sa.SubagentType,
			Done:        sa.CompletedAt != nil,
			DurationMs:  sa.DurationMs,
		}
		if !sa.StartedAt.IsZero() {
			si.StartedAtUnix = sa.StartedAt.Unix()
		}
		ai.Subagents = append(ai.Subagents, si)
	}
}

// modelUsages flattens the usage summary into a stable, non-empty-only model
// list. Nil or zero-total models are skipped so the frontend never draws an
// empty bar.
func modelUsages(summary *claude.UsageSummary) []ModelUsage {
	out := []ModelUsage{}
	if summary == nil {
		return out
	}
	for name, mu := range summary.Models {
		if mu == nil || mu.Total() == 0 {
			continue
		}
		out = append(out, ModelUsage{Name: name, InputTokens: mu.InputTokens, OutputTokens: mu.OutputTokens})
	}
	return out
}

// statusString maps the internal SessionStatus enum to the lowercase token the
// frontend switches on for icon and colour.
func statusString(s logparser.SessionStatus) string {
	switch s {
	case logparser.StatusWorking:
		return "working"
	case logparser.StatusBlocked:
		return "blocked"
	case logparser.StatusWaiting:
		return "waiting"
	case logparser.StatusError:
		return "error"
	case logparser.StatusEnded:
		return "ended"
	default:
		return "ready"
	}
}
