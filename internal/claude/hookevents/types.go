package hookevents

import (
	"time"

	"github.com/gethuman-sh/human/internal/claude/logparser"
)

// Event represents a single hook event line from events.jsonl.
type Event struct {
	EventName        string    `json:"event"`
	SessionID        string    `json:"session_id"`
	Cwd              string    `json:"cwd"`
	Timestamp        time.Time `json:"timestamp"`
	NotificationType string    `json:"notification_type,omitempty"`
	ToolName         string    `json:"tool_name,omitempty"`
	ErrorType        string    `json:"error_type,omitempty"`
	AgentName        string    `json:"agent_name,omitempty"`
	// ToolInput is the bounded, redacted JSON of the tool's input — what the
	// tool read or ran, not merely that it ran (SC-2461).
	ToolInput string `json:"tool_input,omitempty"`
	// SubagentType and Model identify which tier ran a stage (SubagentStart).
	SubagentType string `json:"subagent_type,omitempty"`
	Model        string `json:"model,omitempty"`
	// DurationMs is the tool's wall time, derived by pairing Post→Pre daemon-side.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// RunID is the token the daemon minted when it launched this run and injected
	// into the container. It is what lets the daemon act on ITS OWN launches
	// rather than on whatever a caller names: AgentName is filled from an
	// environment variable inside the container and identifies the work only by
	// convention, while a RunID the daemon does not hold in its own registry is,
	// by construction, not a run it started (SC-4082). Empty from a container
	// launched before this existed.
	RunID string `json:"run_id,omitempty"`
}

// SessionSnapshot holds the derived working/idle state for one session.
type SessionSnapshot struct {
	SessionID   string                  `json:"session_id"`
	Cwd         string                  `json:"cwd"`
	Status      logparser.SessionStatus `json:"status"`
	LastEventAt time.Time               `json:"last_event_at"`
	CurrentTool string                  `json:"current_tool,omitempty"`
	BlockedTool string                  `json:"blocked_tool,omitempty"`
	ErrorType   string                  `json:"error_type,omitempty"`
}
