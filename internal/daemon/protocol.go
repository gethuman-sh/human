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
	// ReadyForReviewPRs maps an engineering ticket key to the pull-request URL
	// carried on its handoff comment's optional `pr:` line, when present.
	ReadyForReviewPRs map[string]string `json:"ready_for_review_prs,omitempty"`
	// BoardCards is the derived pipeline placement per PM issue key, for the
	// drag-board GUI. It is PM-role-only (maps a PM issue key → its derived
	// BoardCard) and is left nil on engineering-tracker results.
	BoardCards map[string]BoardCard `json:"board_cards,omitempty"`
	Err        string               `json:"error,omitempty"`
}

// IssueDetailRequest asks for one full ticket by key. Tracker and Kind are the
// instance name and provider kind the issue was listed from
// (TrackerIssuesResult.TrackerName/TrackerKind), so the daemon resolves the
// exact instance instead of guessing — bare numeric keys are ambiguous across
// kinds, and a name alone is too: different provider sections may configure
// the same instance name (e.g. a gitlab and a shortcut both named "human").
type IssueDetailRequest struct {
	Tracker string `json:"tracker"`
	Kind    string `json:"kind,omitempty"`
	Key     string `json:"key"`
}

// IssueDetailResult is the tracker-issue route's response: the full issue plus
// a display-ready HTML rendering of its markdown description. The daemon owns
// the rendering (goldmark + bluemonday sanitization) so every client renders
// tracker content identically and none of them ever injects unsanitized HTML
// into a webview.
type IssueDetailResult struct {
	tracker.Issue
	DescriptionHTML string `json:"description_html,omitempty"`
	// Comment-sourced sections, pre-rendered to sanitized HTML by the daemon
	// (goldmark + bluemonday), so every client injects them verbatim. Empty
	// when the ticket has no such comment (or comments could not be fetched).
	ReviewFindingsHTML string `json:"review_findings_html,omitempty"`
	FailureReasonHTML  string `json:"failure_reason_html,omitempty"`
	FixSummaryHTML     string `json:"fix_summary_html,omitempty"`
}

// IssueDetailFetch is what the daemon's issue getter returns: the full issue
// plus the comment-sourced extras. Extras are best-effort — a comment-fetch
// failure leaves them zero-valued and the issue still returns (readable beats
// gone).
type IssueDetailFetch struct {
	Issue  tracker.Issue
	Extras IssueDetailExtras
}

// Request is sent from the client to the daemon (one JSON line per connection).
type Request struct {
	Version string `json:"version"`
	// Protocol is the wire protocol the client speaks (the Protocol constant
	// of its build). Zero means a pre-handshake client; the daemon then falls
	// back to the legacy version-string gate.
	Protocol  int               `json:"protocol,omitempty"`
	Token     string            `json:"token"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env,omitempty"`
	ClientPID int               `json:"client_pid,omitempty"` // parent PID (Claude process) for connection tracking
	Cwd       string            `json:"cwd,omitempty"`        // client working directory for project routing
	// Stdin is the client's piped standard input, forwarded because the daemon
	// executes the command in its own process and would otherwise hand it the
	// daemon's stdin — so every `--body-file -` silently read nothing. Empty
	// when the client's stdin is a terminal or carries no data.
	Stdin string `json:"stdin,omitempty"`
	// ConfirmID is a client-generated unique ID for destructive operations.
	// It keys the daemon's confirmation queue, makes resubmits idempotent,
	// and lets the client query the decision later via confirm-status.
	ConfirmID string `json:"confirm_id,omitempty"`
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

// ConfirmStatus is the wire type returned by the confirm-status route: the
// decision state of a queued destructive-operation permission request.
type ConfirmStatus struct {
	ID         string `json:"id"`
	State      string `json:"state"` // pending, approved, denied, unknown
	Prompt     string `json:"prompt,omitempty"`
	ResolvedAt string `json:"resolved_at,omitempty"`
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
