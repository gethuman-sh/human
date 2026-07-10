package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// IdeationState is the lifecycle state of the single board ideation session.
type IdeationState string

const (
	IdeationNone             IdeationState = "none"              // no session exists
	IdeationThinking         IdeationState = "thinking"          // agent turn in flight
	IdeationAwaitingReply    IdeationState = "awaiting_reply"    // agent asked, user must answer
	IdeationAwaitingApproval IdeationState = "awaiting_approval" // guided mode only: draft ready for user edit/submit
	IdeationDone             IdeationState = "done"              // ticket created
	IdeationError            IdeationState = "error"             // turn or creation failed
)

// IdeationMessage is one transcript entry. Role is "user" or "agent".
type IdeationMessage struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// IdeationMode selects which agent prompt/turn discipline the session runs.
// Chat mode (the HUM-152 default) is unchanged by this ticket; guided mode is
// additive.
type IdeationMode string

const (
	IdeationModeChat   IdeationMode = "chat"
	IdeationModeGuided IdeationMode = "guided"
)

// IdeationQuestion is one guided-mode multiple-choice question. Kind
// distinguishes a fixed-option structural question from an agent-generated
// content question, purely for frontend styling/copy — both always carry a
// freeform escape hatch on the client side regardless of Kind.
type IdeationQuestion struct {
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Kind    string   `json:"kind"` // "structural" | "content"
}

// IdeationDraft is the agent-drafted ticket summary shown for review/edit in
// guided mode before submission.
type IdeationDraft struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// IdeationStatus is the wire snapshot returned by all ideation routes.
type IdeationStatus struct {
	SessionID  string            `json:"session_id,omitempty"`
	Mode       IdeationMode      `json:"mode,omitempty"`
	State      IdeationState     `json:"state"`
	Transcript []IdeationMessage `json:"transcript,omitempty"`
	Question   *IdeationQuestion `json:"question,omitempty"` // set only while State==IdeationAwaitingReply in guided mode
	Draft      *IdeationDraft    `json:"draft,omitempty"`    // set only while State==IdeationAwaitingApproval
	CreatedKey string            `json:"created_key,omitempty"`
	CreatedURL string            `json:"created_url,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// IdeationStartRequest is the wire request for ideation-start. Seed is the
// user's initial idea text. Restart abandons an active session first.
type IdeationStartRequest struct {
	Seed    string       `json:"seed"`
	Mode    IdeationMode `json:"mode,omitempty"` // defaults to IdeationModeChat when empty
	Restart bool         `json:"restart,omitempty"`
}

// IdeationReplyRequest is the wire request for ideation-reply.
type IdeationReplyRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// IdeationApproveRequest carries the user's (possibly edited) final draft for
// ticket creation. SessionID must match the session currently in
// IdeationAwaitingApproval.
type IdeationApproveRequest struct {
	SessionID   string `json:"session_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// IdeationTurn is one completed headless agent turn.
type IdeationTurn struct {
	Reply    string // agent's text output for this turn
	ResumeID string // provider session id to resume the next turn
}

// IdeationRunner runs one headless agent turn on the daemon host. resumeID is
// empty on the first turn. Implementations must be safe for sequential reuse.
type IdeationRunner interface {
	Run(ctx context.Context, resumeID, prompt string) (IdeationTurn, error)
}

// PMCreatorResolver resolves the single PM-role tracker Creator and its first
// configured project, mirroring the role-based resolution of resolvePMCommenter.
type PMCreatorResolver func() (creator tracker.Creator, project string, err error)

// ideationSession is the engine's internal mutable state (guarded by engine mu).
type ideationSession struct {
	id              string
	mode            IdeationMode
	state           IdeationState
	transcript      []IdeationMessage
	resumeID        string
	question        *IdeationQuestion
	draft           *IdeationDraft
	createdKey      string
	createdURL      string
	errMsg          string
	repairAttempted bool // one corrective turn allowed per malformed ticket OR question block
}

// IdeationEngine owns the single board ideation session. All exported methods
// are safe for concurrent use.
type IdeationEngine struct {
	Runner         IdeationRunner
	ResolveCreator PMCreatorResolver
	Notify         func()        // pokes the subscribe loop after ticket creation; nil ok
	TurnTimeout    time.Duration // defaults to 5 * time.Minute when zero
	Logger         zerolog.Logger

	mu   sync.Mutex
	sess *ideationSession
}

// turnTimeout returns the configured turn timeout, defaulting to 5 minutes so
// a hung headless agent process cannot pin a session in "thinking" forever.
func (e *IdeationEngine) turnTimeout() time.Duration {
	if e.TurnTimeout > 0 {
		return e.TurnTimeout
	}
	return 5 * time.Minute
}

// snapshot builds the wire status from the current session state. Caller must
// hold mu.
func (e *IdeationEngine) snapshot() IdeationStatus {
	if e.sess == nil {
		return IdeationStatus{State: IdeationNone}
	}
	s := e.sess
	return IdeationStatus{
		SessionID:  s.id,
		Mode:       s.mode,
		State:      s.state,
		Transcript: append([]IdeationMessage(nil), s.transcript...),
		Question:   s.question,
		Draft:      s.draft,
		CreatedKey: s.createdKey,
		CreatedURL: s.createdURL,
		Error:      s.errMsg,
	}
}

// Start begins a new session (or re-attaches to the active one).
func (e *IdeationEngine) Start(req IdeationStartRequest) (IdeationStatus, error) {
	if strings.TrimSpace(req.Seed) == "" {
		return IdeationStatus{}, errors.WithDetails("ideation seed must not be empty")
	}

	e.mu.Lock()
	// An active, non-terminal session is re-attached rather than replaced
	// unless the caller explicitly asks to restart — this is what makes
	// panel close/reopen idempotent (AD-4).
	if e.sess != nil && !req.Restart &&
		(e.sess.state == IdeationThinking || e.sess.state == IdeationAwaitingReply) {
		snap := e.snapshot()
		e.mu.Unlock()
		return snap, nil
	}

	mode := req.Mode
	if mode == "" {
		mode = IdeationModeChat
	}

	now := time.Now()
	sess := &ideationSession{
		id:    fmt.Sprintf("ideation-%d", now.UnixNano()),
		mode:  mode,
		state: IdeationThinking,
		transcript: []IdeationMessage{
			{Role: "user", Text: req.Seed, Time: now},
		},
	}
	e.sess = sess
	snap := e.snapshot()
	e.mu.Unlock()

	if mode == IdeationModeGuided {
		go e.runTurn(sess.id, "", guidedIdeationPrompt(req.Seed))
	} else {
		go e.runTurn(sess.id, "", ideationPrompt(req.Seed))
	}
	return snap, nil
}

// Reply feeds the user's answer into the running session.
func (e *IdeationEngine) Reply(req IdeationReplyRequest) (IdeationStatus, error) {
	if strings.TrimSpace(req.Message) == "" {
		return IdeationStatus{}, errors.WithDetails("ideation reply message must not be empty")
	}

	e.mu.Lock()
	if e.sess == nil || req.SessionID != e.sess.id {
		e.mu.Unlock()
		return IdeationStatus{}, errors.WithDetails("no matching ideation session", "session", req.SessionID)
	}
	if e.sess.state != IdeationAwaitingReply {
		state := e.sess.state
		e.mu.Unlock()
		return IdeationStatus{}, errors.WithDetails("ideation session is not awaiting a reply", "state", string(state))
	}

	e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "user", Text: req.Message, Time: time.Now()})
	e.sess.state = IdeationThinking
	resumeID := e.sess.resumeID
	sessID := e.sess.id
	snap := e.snapshot()
	e.mu.Unlock()

	go e.runTurn(sessID, resumeID, req.Message)
	return snap, nil
}

// Status returns the current snapshot; State==IdeationNone when no session.
func (e *IdeationEngine) Status() IdeationStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshot()
}

// Approve submits the user's (possibly edited) guided-mode draft for ticket
// creation. Only valid while the session is IdeationAwaitingApproval.
func (e *IdeationEngine) Approve(req IdeationApproveRequest) (IdeationStatus, error) {
	if strings.TrimSpace(req.Title) == "" {
		return IdeationStatus{}, errors.WithDetails("ideation approve title must not be empty")
	}
	e.mu.Lock()
	if e.sess == nil || req.SessionID != e.sess.id {
		e.mu.Unlock()
		return IdeationStatus{}, errors.WithDetails("no matching ideation session", "session", req.SessionID)
	}
	if e.sess.state != IdeationAwaitingApproval {
		state := e.sess.state
		e.mu.Unlock()
		return IdeationStatus{}, errors.WithDetails("ideation session is not awaiting approval", "state", string(state))
	}
	sessID := e.sess.id
	e.mu.Unlock()
	e.createTicket(sessID, req.Title, req.Description)
	e.mu.Lock()
	snap := e.snapshot()
	e.mu.Unlock()
	return snap, nil
}

// runTurn executes one headless agent turn and applies its result to the
// session. It runs in its own goroutine so Start/Reply return immediately
// (AD-3: async turns + status polling, not connection streaming).
func (e *IdeationEngine) runTurn(sessID, resumeID, prompt string) {
	ctx, cancel := context.WithTimeout(context.Background(), e.turnTimeout())
	defer cancel()

	turn, err := e.Runner.Run(ctx, resumeID, prompt)

	e.mu.Lock()
	// The session may have been restarted or replaced while the turn was in
	// flight; a stale result must not clobber the new session's state.
	if e.sess == nil || e.sess.id != sessID {
		e.mu.Unlock()
		return
	}

	if err != nil {
		e.Logger.Error().Fields(errors.AllDetails(err)).Msg(errors.CauseChain(err))
		e.sess.state = IdeationError
		e.sess.errMsg = err.Error()
		e.mu.Unlock()
		return
	}

	e.sess.resumeID = turn.ResumeID

	ticket, ticketFound, ticketErr := parseTicketBlock(turn.Reply)
	switch {
	case ticketFound && ticketErr == nil && e.sess.mode == IdeationModeGuided:
		// Guided mode routes a valid ticket block to a review/edit step
		// instead of creating the ticket immediately.
		stripped := strings.TrimSpace(ticketBlockRe.ReplaceAllString(turn.Reply, ""))
		if stripped != "" {
			e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: stripped, Time: time.Now()})
		}
		e.sess.draft = &IdeationDraft{Title: ticket.Title, Description: ticket.Description}
		e.sess.question = nil
		e.sess.state = IdeationAwaitingApproval
		e.mu.Unlock()
	case ticketFound && ticketErr == nil:
		// Chat mode keeps auto-creating the ticket the instant a valid block
		// is parsed — unchanged from HUM-152.
		stripped := strings.TrimSpace(ticketBlockRe.ReplaceAllString(turn.Reply, ""))
		if stripped != "" {
			e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: stripped, Time: time.Now()})
		}
		e.mu.Unlock()
		e.createTicket(sessID, ticket.Title, ticket.Description)
	case ticketFound:
		e.applyRepairOrError(sessID, turn.Reply, ideationRepairPrompt, "agent emitted a malformed ticket block")
	case e.sess.mode == IdeationModeGuided:
		e.applyGuidedTurnResult(sessID, turn.Reply)
	default:
		// Plain free text with no marker of either kind — the normal
		// question/answer turn in chat mode.
		e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: turn.Reply, Time: time.Now()})
		e.sess.state = IdeationAwaitingReply
		e.sess.repairAttempted = false
		e.mu.Unlock()
	}
}

// applyGuidedTurnResult applies a guided-mode turn's reply once it is known
// the ticket marker was not present. It handles the question-block marker,
// falling back to the plain free-text branch when no marker is present at
// all (prompt-discipline failure) so the user always sees visible progress
// rather than a silent hang. Caller must hold mu and must not have unlocked
// it yet; this function always unlocks before returning.
func (e *IdeationEngine) applyGuidedTurnResult(sessID, reply string) {
	q, found, err := parseQuestionBlock(reply)
	switch {
	case found && err == nil:
		e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: q.Text, Time: time.Now()})
		e.sess.question = &q
		e.sess.state = IdeationAwaitingReply
		e.sess.repairAttempted = false
		e.mu.Unlock()
	case found:
		e.applyRepairOrError(sessID, reply, ideationQuestionRepairPrompt, "agent emitted a malformed question block")
	default:
		// Safety net: agent forgot to emit a marker at all. Surface the raw
		// reply as a normal transcript message with the free-text input
		// enabled instead of hanging silently.
		e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: reply, Time: time.Now()})
		e.sess.question = nil
		e.sess.state = IdeationAwaitingReply
		e.sess.repairAttempted = false
		e.mu.Unlock()
	}
}

// applyRepairOrError sends the one-shot repair turn for a malformed marker
// block, or gives up and marks the session errored once the repair budget is
// exhausted. Caller must hold mu; this function always unlocks before
// returning.
func (e *IdeationEngine) applyRepairOrError(sessID, reply, repairPrompt, errMsg string) {
	if !e.sess.repairAttempted {
		e.sess.repairAttempted = true
		resume := e.sess.resumeID
		e.sess.state = IdeationThinking
		e.mu.Unlock()
		go e.runTurn(sessID, resume, repairPrompt)
		return
	}
	e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: reply, Time: time.Now()})
	e.sess.state = IdeationError
	e.sess.errMsg = errMsg
	e.mu.Unlock()
}

// createTicket calls the tracker Creator to materialize the PM ticket. Run
// without holding mu so the network call cannot block Status()/Reply().
func (e *IdeationEngine) createTicket(sessID, title, description string) {
	if e.ResolveCreator == nil {
		e.mu.Lock()
		if e.sess != nil && e.sess.id == sessID {
			e.sess.state = IdeationError
			e.sess.errMsg = "no PM ticket creator configured"
		}
		e.mu.Unlock()
		return
	}

	creator, project, err := e.ResolveCreator()
	if err != nil {
		e.mu.Lock()
		if e.sess != nil && e.sess.id == sessID {
			e.sess.state = IdeationError
			e.sess.errMsg = err.Error()
		}
		e.mu.Unlock()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	created, err := creator.CreateIssue(ctx, &tracker.Issue{Project: project, Title: title, Description: description})

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess == nil || e.sess.id != sessID {
		return
	}
	if err != nil {
		e.sess.state = IdeationError
		e.sess.errMsg = errors.WrapWithDetails(err, "creating PM ticket from ideation", "project", project).Error()
		return
	}
	e.sess.state = IdeationDone
	e.sess.createdKey = created.Key
	e.sess.createdURL = created.URL
	e.sess.transcript = append(e.sess.transcript, IdeationMessage{
		Role: "agent",
		Text: "Created ticket " + created.Key,
		Time: time.Now(),
	})
	if e.Notify != nil {
		e.Notify()
	}
}

// ideationTicketMarker is the line the agent emits when confident; the JSON
// payload follows in a fenced block. Marker style matches the [human:*]
// comment-marker convention used across the board pipeline.
const ideationTicketMarker = "[human:ideation-ticket]"

var ticketBlockRe = regexp.MustCompile(
	`(?s)\[human:ideation-ticket\]\s*` + "```(?:json)?\\s*(\\{.*?\\})\\s*```")

type ideationTicket struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// parseTicketBlock extracts the final ticket block from an agent reply.
// found is true when the marker is present; err reports a malformed payload
// (bad JSON or empty title).
func parseTicketBlock(reply string) (t ideationTicket, found bool, err error) {
	if !strings.Contains(reply, ideationTicketMarker) {
		return ideationTicket{}, false, nil
	}
	m := ticketBlockRe.FindStringSubmatch(reply)
	if m == nil {
		return ideationTicket{}, true, errors.WithDetails("ideation ticket marker present but no fenced JSON block found")
	}
	if jsonErr := json.Unmarshal([]byte(m[1]), &t); jsonErr != nil {
		return ideationTicket{}, true, errors.WrapWithDetails(jsonErr, "invalid ideation ticket JSON")
	}
	if strings.TrimSpace(t.Title) == "" {
		return ideationTicket{}, true, errors.WithDetails("ideation ticket JSON missing title")
	}
	return t, true, nil
}

// ideationQuestionMarker is the line a guided-mode agent emits when asking a
// structured multiple-choice question; the JSON payload follows in a fenced
// block, parallel to ideationTicketMarker.
const ideationQuestionMarker = "[human:ideation-question]"

var questionBlockRe = regexp.MustCompile(
	`(?s)\[human:ideation-question\]\s*` + "```(?:json)?\\s*(\\{.*?\\})\\s*```")

type ideationQuestionPayload struct {
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Kind    string   `json:"kind"`
}

// parseQuestionBlock extracts a guided-mode question block from an agent
// reply. found is true when the marker is present; err reports a malformed
// payload (bad JSON, empty text, or fewer than two options). Uses
// FindStringSubmatch (first-match) to match parseTicketBlock's actual
// behavior — exactly one question marker is expected per reply by prompt
// design.
func parseQuestionBlock(reply string) (q IdeationQuestion, found bool, err error) {
	if !strings.Contains(reply, ideationQuestionMarker) {
		return IdeationQuestion{}, false, nil
	}
	m := questionBlockRe.FindStringSubmatch(reply)
	if m == nil {
		return IdeationQuestion{}, true, errors.WithDetails("ideation question marker present but no fenced JSON block found")
	}
	var payload ideationQuestionPayload
	if jsonErr := json.Unmarshal([]byte(m[1]), &payload); jsonErr != nil {
		return IdeationQuestion{}, true, errors.WrapWithDetails(jsonErr, "invalid ideation question JSON")
	}
	if strings.TrimSpace(payload.Text) == "" {
		return IdeationQuestion{}, true, errors.WithDetails("ideation question JSON missing text")
	}
	if len(payload.Options) < 2 {
		return IdeationQuestion{}, true, errors.WithDetails("ideation question JSON must have at least two options")
	}
	return IdeationQuestion(payload), true, nil
}

// ideationRepairPrompt is the one-shot corrective turn sent when the agent
// emitted the marker with a malformed payload. Sent via --resume, so the
// agent retains full conversation context.
const ideationRepairPrompt = "Your previous message contained the " +
	"[human:ideation-ticket] marker but the JSON block was malformed or " +
	"missing a title. Re-emit ONLY the line [human:ideation-ticket] followed " +
	"by a fenced json code block containing exactly " +
	`{"title": "...", "description": "..."}` + " — no other text."

// ideationPrompt builds the first-turn prompt for a headless ideation agent,
// condensed from the /human-ideate skill discipline and adapted for a
// multi-turn, one-question-per-turn headless loop.
func ideationPrompt(seed string) string {
	var b strings.Builder
	b.WriteString("You are an ideation agent challenging a rough product idea before it becomes a PM ticket. ")
	b.WriteString("You may read the repository with read-only tools for context.\n\n")
	b.WriteString("Ask exactly ONE forcing/challenge question per turn and then stop; the user's next message is the answer. ")
	b.WriteString("Challenge the premise, probe scope (expand/hold/reduce), and push for a high-confidence problem statement. ")
	b.WriteString("Ask at most 7 questions; stop earlier once confidence is high.\n\n")
	b.WriteString("When (and only when) confident, output the line `[human:ideation-ticket]` followed by a fenced ```json block ")
	b.WriteString(`containing exactly {"title": "...", "description": "..."} where description is Markdown with ` +
		"`## Problem Statement`, `## User Story`, `## Acceptance Criteria`, and `## Scope Decisions` sections. ")
	b.WriteString("Do NOT create the ticket yourself, do not run any commands that modify anything, and do not emit the marker before you are confident.\n\n")
	b.WriteString("The idea: " + seed)
	return b.String()
}

// ideationQuestionRepairPrompt is the one-shot corrective turn sent when the
// agent emitted the question marker with a malformed payload. Sent via
// --resume, so the agent retains full conversation context.
const ideationQuestionRepairPrompt = "Your previous message contained the " +
	"[human:ideation-question] marker but the JSON block was malformed, " +
	"missing text, or had fewer than two options. Re-emit ONLY the line " +
	"[human:ideation-question] followed by a fenced json code block containing exactly " +
	`{"text": "...", "options": ["...", "..."], "kind": "structural"|"content"}` + " — no other text."

// guidedIdeationPrompt builds the first-turn prompt for a headless
// guided-mode ideation agent: instead of open-ended free text, each turn
// must ask exactly one multiple-choice question via the
// [human:ideation-question] marker, until the agent is confident enough to
// emit the same [human:ideation-ticket] marker chat mode uses.
func guidedIdeationPrompt(seed string) string {
	var b strings.Builder
	b.WriteString("You are an ideation agent gathering context for a PM ticket via a guided, ")
	b.WriteString("multiple-choice question flow. You may read the repository with read-only tools for context.\n\n")
	b.WriteString("Ask exactly ONE multiple-choice question per turn and then stop; the user's next message is the answer. ")
	b.WriteString("Emit each question as the line `[human:ideation-question]` followed by a fenced ```json block ")
	b.WriteString(`containing exactly {"text": "...", "options": ["...", "..."], "kind": "structural"|"content"}. `)
	b.WriteString("Provide 3-5 options per question. Do NOT emit any other text alongside the marker/JSON block.\n\n")
	b.WriteString("Structural questions (kind=\"structural\") cover scope and tracker/project decisions and must use a small, ")
	b.WriteString("fixed option set: for the scope decision, the options are exactly \"Expand\", \"Hold\", \"Reduce\"; ")
	b.WriteString("for the tracker/project decision, state the single resolved PM tracker and project as the sole option ")
	b.WriteString("(plus the freeform escape hatch the client always shows) — do not invent additional trackers/projects.\n\n")
	b.WriteString("Content questions (kind=\"content\") cover problem framing, persona, and acceptance criteria; ")
	b.WriteString("derive their options yourself from the repository/session context gathered so far — do not use static/canned text.\n\n")
	b.WriteString("Follow the same forcing-question shape as human-ideate: pain, who, status quo, wedge, scope. ")
	b.WriteString("Ask at most 7 questions; stop earlier once confidence is high.\n\n")
	b.WriteString("When (and only when) confident, output the line `[human:ideation-ticket]` followed by a fenced ```json block ")
	b.WriteString(`containing exactly {"title": "...", "description": "..."} where description is Markdown with ` +
		"`## Problem Statement`, `## User Story`, `## Acceptance Criteria`, and `## Scope Decisions` sections. ")
	b.WriteString("Do NOT create the ticket yourself, do not run any commands that modify anything, and do not emit the ticket marker before you are confident.\n\n")
	b.WriteString("The idea: " + seed)
	return b.String()
}
