package gui

import (
	"sort"
	"time"

	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/claude/logparser"
	"github.com/gethuman-sh/human/internal/claude/monitor"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
)

// The DTO layer exists because monitor.Snapshot and its claude/logparser
// leaf types carry no JSON tags (and an error field). These view models
// pin the wire format the frontend consumes, independent of internal
// struct evolution. Leaf types that already define JSON tags
// (daemon.NetworkEvent, stats.ToolStats) are reused as-is.

// SnapshotDTO is the wire form of one dashboard refresh.
type SnapshotDTO struct {
	FetchedAt     time.Time             `json:"fetched_at"`
	Hostname      string                `json:"hostname,omitempty"`
	Error         string                `json:"error,omitempty"`
	Daemon        DaemonDTO             `json:"daemon"`
	Telegram      string                `json:"telegram,omitempty"`
	Slack         string                `json:"slack,omitempty"`
	UsageWindow   *UsageWindowDTO       `json:"usage_window,omitempty"`
	Trackers      []TrackerStatusDTO    `json:"trackers,omitempty"`
	Instances     []InstanceDTO         `json:"instances,omitempty"`
	Panes         []PaneDTO             `json:"panes,omitempty"`
	TotalUsage    []ModelUsageDTO       `json:"total_usage,omitempty"`
	NetworkEvents []daemon.NetworkEvent `json:"network_events,omitempty"`
	ToolStats     *stats.ToolStats      `json:"tool_stats,omitempty"`
}

// DaemonDTO is the daemon liveness block of the status line.
type DaemonDTO struct {
	PID              int    `json:"pid"`
	Alive            bool   `json:"alive"`
	ProxyAddr        string `json:"proxy_addr,omitempty"`
	ProxyActiveConns int64  `json:"proxy_active_conns"`
}

// UsageWindowDTO is the current 5-hour usage accounting window.
type UsageWindowDTO struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// TrackerStatusDTO mirrors tracker.TrackerStatus (which has no JSON tags).
type TrackerStatusDTO struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Label    string   `json:"label"`
	Working  bool     `json:"working"`
	VaultRef bool     `json:"vault_ref,omitempty"`
	Missing  []string `json:"missing,omitempty"`
}

// InstanceDTO is one Claude instance card.
type InstanceDTO struct {
	Label           string          `json:"label"`
	Source          string          `json:"source"` // "host" | "container"
	Cwd             string          `json:"cwd,omitempty"`
	PID             int             `json:"pid,omitempty"`
	ContainerID     string          `json:"container_id,omitempty"`
	MemoryUsage     uint64          `json:"memory_usage,omitempty"`
	MemoryLimit     uint64          `json:"memory_limit,omitempty"`
	ProxyConfigured bool            `json:"proxy_configured,omitempty"`
	DaemonConnected bool            `json:"daemon_connected,omitempty"`
	Session         *SessionDTO     `json:"session,omitempty"`
	Models          []ModelUsageDTO `json:"models,omitempty"`
}

// SessionDTO is the parsed JSONL/hook state of an instance.
type SessionDTO struct {
	SessionID    string        `json:"session_id"`
	Slug         string        `json:"slug,omitempty"`
	Status       string        `json:"status"` // "ready" | "working" | "blocked" | "waiting" | "error" | "ended"
	StartedAt    time.Time     `json:"started_at"`
	LastActivity time.Time     `json:"last_activity"`
	CurrentTool  string        `json:"current_tool,omitempty"`
	BlockedTool  string        `json:"blocked_tool,omitempty"`
	ErrorType    string        `json:"error_type,omitempty"`
	Subagents    []SubagentDTO `json:"subagents,omitempty"`
	Tasks        []TaskDTO     `json:"tasks,omitempty"`
}

// SubagentDTO is one spawned subagent row under an instance.
type SubagentDTO struct {
	Description  string     `json:"description"`
	SubagentType string     `json:"subagent_type,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	DurationMs   int64      `json:"duration_ms,omitempty"`
}

// TaskDTO is one background task entry of an instance.
type TaskDTO struct {
	Subject string `json:"subject"`
	Status  string `json:"status"` // "pending" | "in_progress" | "completed"
}

// ModelUsageDTO is the per-model token bar data.
type ModelUsageDTO struct {
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	CacheCreate  int    `json:"cache_create"`
	CacheRead    int    `json:"cache_read"`
}

// PaneDTO is one tmux pane running Claude.
type PaneDTO struct {
	SessionName  string `json:"session_name"`
	WindowIndex  int    `json:"window_index"`
	PaneIndex    int    `json:"pane_index"`
	Cwd          string `json:"cwd,omitempty"`
	Devcontainer bool   `json:"devcontainer,omitempty"`
	State        string `json:"state,omitempty"`
}

// ToSnapshotDTO maps a monitor snapshot to its wire form.
func ToSnapshotDTO(snap *monitor.Snapshot, hostname string) SnapshotDTO {
	if snap == nil {
		return SnapshotDTO{}
	}
	dto := SnapshotDTO{
		FetchedAt: snap.FetchedAt,
		Hostname:  hostname,
		Daemon: DaemonDTO{
			PID:              snap.Daemon.PID,
			Alive:            snap.Daemon.Alive,
			ProxyAddr:        snap.Daemon.ProxyAddr,
			ProxyActiveConns: snap.Daemon.ProxyActiveConns,
		},
		Telegram:      snap.Telegram,
		Slack:         snap.Slack,
		NetworkEvents: snap.NetworkEvents,
		ToolStats:     snap.ToolStats,
	}
	if snap.Err != nil {
		dto.Error = snap.Err.Error()
	}
	if !snap.FetchedAt.IsZero() {
		start := claude.WindowStart(snap.FetchedAt)
		dto.UsageWindow = &UsageWindowDTO{Start: start, End: claude.WindowEnd(start)}
	}
	for _, t := range snap.Trackers {
		dto.Trackers = append(dto.Trackers, TrackerStatusDTO(t))
	}
	for _, iv := range snap.Instances {
		dto.Instances = append(dto.Instances, toInstanceDTO(iv))
	}
	for _, p := range snap.Panes {
		dto.Panes = append(dto.Panes, PaneDTO{
			SessionName:  p.SessionName,
			WindowIndex:  p.WindowIndex,
			PaneIndex:    p.PaneIndex,
			Cwd:          p.Cwd,
			Devcontainer: p.Devcontainer,
			State:        instanceStateString(p.State),
		})
	}
	dto.TotalUsage = toModelUsageDTOs(snap.TotalUsage)
	return dto
}

func toInstanceDTO(iv monitor.InstanceView) InstanceDTO {
	inst := iv.Usage.Instance
	dto := InstanceDTO{
		Label:           inst.Label,
		Source:          inst.Source,
		Cwd:             inst.Cwd,
		PID:             inst.PID,
		ContainerID:     inst.ContainerID,
		ProxyConfigured: inst.ProxyConfigured,
		DaemonConnected: inst.DaemonConnected,
		Models:          toModelUsageDTOs(iv.Usage.Summary),
	}
	if inst.Memory != nil {
		dto.MemoryUsage = inst.Memory.Usage
		dto.MemoryLimit = inst.Memory.Limit
	}
	if iv.Session != nil {
		dto.Session = toSessionDTO(*iv.Session)
	}
	return dto
}

func toSessionDTO(s logparser.SessionState) *SessionDTO {
	dto := &SessionDTO{
		SessionID:    s.SessionID,
		Slug:         s.Slug,
		Status:       sessionStatusString(s.Status),
		StartedAt:    s.StartedAt,
		LastActivity: s.LastActivity,
		CurrentTool:  s.CurrentTool,
		BlockedTool:  s.BlockedTool,
		ErrorType:    s.ErrorType,
	}
	for _, sa := range s.Subagents {
		dto.Subagents = append(dto.Subagents, SubagentDTO{
			Description:  sa.Description,
			SubagentType: sa.SubagentType,
			StartedAt:    sa.StartedAt,
			CompletedAt:  sa.CompletedAt,
			DurationMs:   sa.DurationMs,
		})
	}
	for _, t := range s.Tasks {
		dto.Tasks = append(dto.Tasks, TaskDTO{Subject: t.Subject, Status: t.Status})
	}
	return dto
}

// toModelUsageDTOs flattens the per-model map into a deterministically
// ordered slice; map ordering would make the JSON jitter between polls.
func toModelUsageDTOs(sum *claude.UsageSummary) []ModelUsageDTO {
	if sum == nil || len(sum.Models) == 0 {
		return nil
	}
	out := make([]ModelUsageDTO, 0, len(sum.Models))
	for name, mu := range sum.Models {
		if mu == nil {
			continue
		}
		out = append(out, ModelUsageDTO{
			Model:        name,
			InputTokens:  mu.InputTokens,
			OutputTokens: mu.OutputTokens,
			CacheCreate:  mu.CacheCreate,
			CacheRead:    mu.CacheRead,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

func sessionStatusString(s logparser.SessionStatus) string {
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

func instanceStateString(s claude.InstanceState) string {
	switch s {
	case claude.StateBusy:
		return "busy"
	case claude.StateError:
		return "error"
	case claude.StateBlocked:
		return "blocked"
	case claude.StateWaiting:
		return "waiting"
	case claude.StateReady:
		return "ready"
	default:
		return "unknown"
	}
}

// Compile-time check: TrackerStatusDTO must stay field-compatible with
// tracker.TrackerStatus for the conversion above.
var _ = TrackerStatusDTO(tracker.TrackerStatus{})
