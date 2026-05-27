package daemon

import "github.com/gethuman-sh/human/internal/tracker"

// TrackerIssuesResult is the wire type for a single tracker/project's issues.
//
// ReadyForReview carries the engineering ticket keys that a PM tracker has
// currently flagged for review via a [human:ready-for-review] comment. It is
// populated on engineering-tracker results (where the keys actually live) so
// the TUI can join it against Issues without a separate lookup. See
// cli/CLAUDE.md "Review handoff" for the comment convention.
type TrackerIssuesResult struct {
	TrackerName    string          `json:"tracker_name"`
	TrackerKind    string          `json:"tracker_kind"`
	TrackerRole    string          `json:"tracker_role,omitempty"`
	Project        string          `json:"project"`
	Issues         []tracker.Issue `json:"issues"`
	ReadyForReview []string        `json:"ready_for_review,omitempty"`
	Err            string          `json:"error,omitempty"`
}

// Request is sent from the client to the daemon (one JSON line per connection).
type Request struct {
	Version   string            `json:"version"`
	Token     string            `json:"token"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env,omitempty"`
	ClientPID int               `json:"client_pid,omitempty"` // parent PID (Claude process) for connection tracking
	Cwd       string            `json:"cwd,omitempty"`        // client working directory for project routing
}

// Response is sent from the daemon back to the client (one or more JSON lines per connection).
type Response struct {
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	ExitCode      int    `json:"exit_code"`
	AwaitCallback bool   `json:"await_callback,omitempty"`
	Callback      string `json:"callback,omitempty"`
	AwaitConfirm  bool   `json:"await_confirm,omitempty"`  // line 1: daemon paused, awaiting TUI confirmation
	ConfirmID     string `json:"confirm_id,omitempty"`     // unique identifier for the pending operation
	ConfirmPrompt string `json:"confirm_prompt,omitempty"` // human-readable prompt, e.g. "Delete JIRA-123?"
}

// SubscribeEvent is a notification sent over a persistent subscribe connection.
// For "agent-stopped" events, AgentName identifies the agent to remove
// immediately without waiting for the next discovery cycle.
type SubscribeEvent struct {
	Type      string `json:"type"`            // "change", "agent-stopped"
	AgentName string `json:"agent,omitempty"` // set for agent lifecycle events
}

// PendingConfirm is the wire type for a single pending destructive operation
// awaiting user confirmation via the TUI.
type PendingConfirm struct {
	ID        string `json:"id"`
	Operation string `json:"operation"` // "DeleteIssue", "EditIssue"
	Tracker   string `json:"tracker"`   // tracker kind, e.g. "jira", "linear"
	Key       string `json:"key"`       // issue key, e.g. "KAN-1"
	Prompt    string `json:"prompt"`
	CreatedAt string `json:"created_at"`
	ClientPID int    `json:"client_pid"` // PID of the Claude instance that triggered the operation
}
