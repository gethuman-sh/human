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
	// Truncated is true when the fetch hit the backend's MaxResults cap and more
	// issues existed than were returned — the result is a partial view of the
	// project. The board uses it to refuse pruning local view state (which would
	// erase saved order/hidden flags for tickets past the cap) and to surface a
	// "showing the first N" affordance. See cli/CLAUDE.md and SC-1693.
	Truncated bool   `json:"truncated,omitempty"`
	Err       string `json:"error,omitempty"`
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

// CurrentUserResult carries the authenticated user's display name for the
// board's ownership dimming. Empty Name means the PM tracker could not (or does
// not) resolve an identity, and the board dims nothing.
type CurrentUserResult struct {
	Name string `json:"name"`
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

// --- Board view (the composed, frontend-facing board) ---
//
// These live here, beside TrackerIssuesResult and BoardCard, because they are
// WIRE types: the daemon composes the board and returns it. They cannot live in
// internal/board, which already imports this package — that would cycle.
// The desktop aliases them (BoardData/Card) so its code and the frontend's JSON
// shape are unchanged.
// Card is the flat, frontend-facing shape of one board ticket: a PM issue joined
// with its derived BoardCard. The frontend renders columns purely from these —
// it never re-derives a stage from comments.
type BoardViewCard struct {
	Key            string `json:"key"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	Stage          string `json:"stage"`
	State          string `json:"state"`
	EngineeringKey string `json:"engineeringKey,omitempty"`
	Branch         string `json:"branch,omitempty"`
	PRURL          string `json:"prURL,omitempty"`
	Error          string `json:"error,omitempty"`
	// ResumeAt is the absolute RFC3339 instant a paused (outage) card resumes,
	// when the refusal stated one. The frontend formats it in the reader's own
	// timezone. Empty when unknown — the card then reads "paused" with no time.
	ResumeAt string `json:"resumeAt,omitempty"`
	// WaitsFor is the ticket a queued card was told to go second to, when the
	// answer a human picked was a sequencing one. It is what turns the queued
	// badge from "picked up" — a spinner over work nobody started — into the
	// do-nothing register naming what the card is waiting for.
	WaitsFor string `json:"waitsFor,omitempty"`
	// Verdict is the latest review's verdict line; a failing verdict pins the
	// card in the Code lane with a warning instead of letting it advance.
	Verdict string `json:"verdict,omitempty"`
	// Activity is the phase the run itself last recorded (stage.triage,
	// stage.verify, …), and ActivityAt is when. The records already exist —
	// every stage writes one with a timestamp so the next stage can read it
	// back — and the card has simply never shown them. Without it the entire fix
	// run, from triage through the adversarial challenge, the plan, the fix and
	// verification, renders as one unchanging "fixing…" that looks identical at
	// thirty seconds, at fourteen hours, and when the agent behind it died
	// yesterday afternoon. Empty when the run recorded no phase.
	Activity   string `json:"activity,omitempty"`
	ActivityAt string `json:"activityAt,omitempty"`
	// StageDaemonID is the machine that stamped the card's deciding marker — who
	// owns the stage. The daemon already guards on it where it acts on its own
	// (WorkGate.ownedHereOrUnowned refuses to take over a stage a peer stamped),
	// but the fact never reached the reader, and it is the missing input for the
	// question a board with several daemons cannot otherwise answer: no agent
	// running HERE means dead only if this machine owns the stage. Owned
	// elsewhere, it means "being worked somewhere I cannot see", which is not the
	// same thing and must not be rendered as if it were. Empty on a single-daemon
	// install and on threads written before stages were stamped.
	StageDaemonID string `json:"stageDaemonId,omitempty"`
	// VerdictFailed is VerdictFailed(Verdict) computed HERE, so the board never
	// re-derives it. The frontend used to run its own prefix test that matched
	// only "fail", while the daemon's also matches "incomplete" — a verdict the
	// reviewer genuinely posts ("blocks deploy and loops the work back to be
	// built, exactly like a fail"). The two answers disagreed by one word: the
	// board put the card in Deploy and withheld the rework gesture while the
	// daemon refused every drop, leaving the card with no move that could
	// succeed. Shipping the decision instead of the input leaves the board
	// nothing to get wrong.
	VerdictFailed bool `json:"verdictFailed,omitempty"`
	// ShippedPartial / ShippedPartialFollowOn surface the durable shipped-partial
	// trace on the card (SC-2910): the ticket shipped with acceptance criteria
	// deferred to the named follow-on ticket. The frontend renders a "partial
	// scope → <key>" badge and a detail-panel section; both empty/false on every
	// card without the marker. Populated by the explicit field copy in compose.go.
	ShippedPartial         bool   `json:"shippedPartial,omitempty"`
	ShippedPartialFollowOn string `json:"shippedPartialFollowOn,omitempty"`
	// StageEnteredAt is when the newest marker of the card's current stage
	// landed (RFC3339); the board's age badge renders how long the card has
	// been sitting. Empty when the card has no derived stage yet.
	StageEnteredAt string `json:"stageEnteredAt,omitempty"`
	// DeployPhase names the done-stage sub-phase of a running card: "pr-review"
	// while the machine reviewer runs, "pr-fix" while the fixer runs, empty for
	// a plain deploy. Both halves of the loop are named because they are
	// separate agents doing opposite work — a card that says "PR review…" while
	// the fixer runs sends a reader to the wrong log (SC-3569). Populated by the
	// explicit field copy below — the daemon→desktop hop is a Go copy, not a
	// JSON re-tag.
	DeployPhase string `json:"deployPhase,omitempty"`
	// RunningStage names another stage of a FAILED card's ticket whose own
	// newest marker is a start — the run the card's single (stage, state) pair
	// cannot show. The viewer joins it into the liveness question so a red card
	// with a live agent behind it renders as work in progress, and the badge
	// names which stage is working (SC-4406). Empty on every card that is not
	// failed. Populated by the explicit field copy in compose.go.
	RunningStage string `json:"runningStage,omitempty"`
	// Degraded marks a card whose markers could not be read this scan (a
	// ListComments error). The frontend renders it locked — non-draggable and
	// non-launchable — so a transient fetch failure never presents as idle,
	// actionable Backlog work (1700).
	Degraded bool `json:"degraded,omitempty"`
	// Labels and Description feed the Ideas→Backlog promotion: labels tell
	// the evolve session which idea labels to remove, the description seeds
	// the ideation conversation alongside the title.
	Labels      []string `json:"labels,omitempty"`
	Description string   `json:"description,omitempty"`
	// Assignee is the ticket owner shown in the detail panel. Display-only:
	// the board never assigns; empty renders as "Unassigned" in the frontend.
	Assignee string `json:"assignee,omitempty"`
	// Reporter is the ticket's filer. The board uses it as the ownership
	// fallback when Assignee is empty (nearly every ticket, since the pipeline
	// sets no assignee), so a card can still be attributed to a person. Display
	// name, same space as Assignee. Populated by the field copy in compose.go.
	Reporter string `json:"reporter,omitempty"`
	// Blockers names the tickets this card is waiting for, so a card that will
	// not start says why on its face rather than looking idle. A blocker and
	// the card it holds usually sit in different columns, which is why this is
	// a badge naming the key and not a line drawn between two cards.
	Blockers []string `json:"blockers,omitempty"`
	// BlockersOffBoard names blockers this board cannot show: on another
	// tracker, or past the fetch cap. They were dropped, so a card with a real
	// open dependency rendered as unblocked — a fact about the view told as a
	// fact about the work (SC-4151 E11). Kept apart from Blockers because the
	// arrow layer can only link keys it can find, so these are named on the card
	// rather than pointed at.
	BlockersOffBoard []string `json:"blockersOffBoard,omitempty"`
	// Tracker/TrackerKind are the instance name and provider kind the issue
	// was listed from. The detail panel passes them back to GetIssueDetail so
	// the daemon resolves the exact instance — bare numeric keys are ambiguous
	// across kinds, and names can repeat across provider sections.
	Tracker     string `json:"tracker,omitempty"`
	TrackerKind string `json:"trackerKind,omitempty"`
	// IdeaColumn is the idea-space sub-column (0 loosest … 4 most concrete)
	// for cards in the Ideas stage. Locally persisted preference, never
	// tracker state; the zero value is the leftmost column, so an idea with
	// no saved placement starts loose by default.
	IdeaColumn int `json:"ideaColumn"`
	// Bug marks a defect ticket (bug label or bug issue type, see
	// tracker.Issue.IsBug). Bug cards render in the Bugs pane instead of the
	// workflow board's columns.
	Bug bool `json:"bug,omitempty"`
	// Security marks a security ticket (security label or type, see
	// tracker.Issue.IsSecurity). Security cards render in the Security half of
	// the Bugs pane. A ticket is never both a bug and security — the tokens are
	// disjoint — so the two flags are mutually exclusive.
	Security bool `json:"security,omitempty"`
	// HasRelatedRecord reports a completed filing-time related-work record
	// ([human:related] found/none). The Bugs pane suppresses the on-demand
	// "Find related work" menu item when true (SC-2405).
	HasRelatedRecord bool `json:"hasRelatedRecord,omitempty"`
	// Hidden marks a ticket the user parked off the board (right-click →
	// Hide). Locally persisted view preference, never tracker state; the
	// frontend filters hidden cards out unless the user reveals them.
	Hidden bool `json:"hidden,omitempty"`
	// NotMine marks a card whose owner (Assignee, or Reporter when there is no
	// assignee) is someone other than the current viewer. A viewer-local flag
	// like Hidden — filled by the desktop overlay (applyLocal), never by
	// Compose — so the frontend can render it dimmed-but-readable. False when
	// the card is the viewer's own, has no owner, or identity is unknown: those
	// all render at full opacity (dimming is a hint, never applied on a guess).
	NotMine bool `json:"notMine,omitempty"`
	// AgentLiveness answers "is an agent actually working this card right now":
	// AgentLive, AgentDead, AgentRecovering, AgentElsewhere, or "" for unknown.
	// AgentRecovering is AgentDead's softer sibling: the agent is gone, but the
	// daemon's own StuckRunningGrace relaunch is not yet due for this card's
	// class, so it must render in the machine register rather than ask a
	// person to retry work the machine hasn't had its turn at yet (SC-3569 PR
	// review finding). A viewer-local field like NotMine — filled by the
	// desktop overlay (applyLocal via board.MarkAgentLiveness), NEVER by
	// Compose, because liveness is a property of the machine looking rather
	// than of the project.
	//
	// "" is the load-bearing value: it means the question could not be answered
	// (no Docker engine here, a discovery error, an unsigned marker, a stage
	// that runs no named agent, or an agent still starting up) and the card must
	// then render exactly as it did before this existed. Absence of a signal is
	// never proof of death.
	AgentLiveness string `json:"agentLiveness,omitempty"`
	// Options carries the card's open decision block: a stage ended in a fork
	// and a human must pick a direction. OptionsContext is the one-line why.
	Options        []BoardOption `json:"options,omitempty"`
	OptionsContext string        `json:"optionsContext,omitempty"`
	// StopDecision/StopLinkedKey/StopReasoning render a pre-planning gate's
	// recorded STOP verdict on the card (superseded/escalated/rejected) so a
	// decided card is distinguishable from one merely waiting. StopDecision is the
	// raw head (frontend maps it to human phrasing); StopLinkedKey is the ticket
	// the decision names (reachable from the card); StopReasoning is the recorded
	// evidence. All empty for a card with no such decision (SC-2699).
	StopDecision  string `json:"stopDecision,omitempty"`
	StopLinkedKey string `json:"stopLinkedKey,omitempty"`
	StopReasoning string `json:"stopReasoning,omitempty"`
	// MockupSlug/MockupState link the card to a locally generated mockup set:
	// "ready" once mockups/<slug>/index.json is valid, "creating" while a
	// launched generation has not produced it yet. Local file state — never
	// tracker state — so browsing or generating mocks leaves no trace on the
	// ticket.
	MockupSlug  string `json:"mockupSlug,omitempty"`
	MockupState string `json:"mockupState,omitempty"`
	// MockupChosenSlug/MockupChosenFile pin the ticket's chosen winner mockup
	// (a leaf group's slug + option file) when one has been marked, so the card
	// can surface that a design direction is selected and the viewer can
	// highlight the root→winner path. Empty when no winner is chosen.
	MockupChosenSlug string `json:"mockupChosenSlug,omitempty"`
	MockupChosenFile string `json:"mockupChosenFile,omitempty"`
}

// BoardData is the full payload the frontend renders: the flat card list plus an
// optional fetch error (surfaced as a banner) and a dockerAvailable flag the
// frontend uses to disable the agent-launching drop targets.
type BoardView struct {
	Cards []BoardViewCard `json:"cards"`
	Error string          `json:"error,omitempty"`
	// Notice is a non-error explanation shown in place of the columns when the
	// board has nothing to render for a structural reason — chiefly no PM-role
	// tracker configured (SC-1655). Distinct from Error so the frontend can
	// style it as guidance rather than a failure.
	Notice string `json:"notice,omitempty"`
	// Truncation is a non-error affordance shown alongside the columns when the
	// fetch hit the backend's cap and more tickets exist than are displayed
	// (SC-1693). Unlike Notice it accompanies a populated board rather than
	// replacing empty columns, so the user knows the list is partial.
	Truncation      string `json:"truncation,omitempty"`
	DockerAvailable bool   `json:"dockerAvailable"`
	// ColumnOrder is the hand-sorted ticket order per queue column (top
	// first). The frontend sorts each column by it; cards absent from their
	// queue's list render after it in fetch order.
	ColumnOrder map[string][]string `json:"columnOrder,omitempty"`
	// DimPercent is how visible a card owned by someone else renders, in
	// percent of full opacity, as declared in .humanconfig's "ui" section
	// (SC-3409). Viewer-local like ColumnOrder: filled by the desktop overlay
	// (applyLocal), never by Compose. ZERO MEANS UNDECLARED and is omitted from
	// the wire, so a project that configured nothing ships exactly today's
	// payload and the frontend leaves the stylesheet's :root fallback alone.
	DimPercent int `json:"dimPercent,omitempty"`
	// CachedAt is when this board was composed, stamped only on the copy kept as
	// the last-good snapshot. A refresh that fails serves that snapshot with a
	// banner saying so — but "the last board that loaded" carries no age, so a
	// snapshot minutes old and one days old read identically while the cards
	// keep their spinners (SC-4151). Empty on every live board and on snapshots
	// written before this existed, which then read exactly as they did.
	CachedAt string `json:"cachedAt,omitempty"`
}
