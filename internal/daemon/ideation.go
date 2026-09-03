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
	IdeationNone          IdeationState = "none"           // no session exists
	IdeationThinking      IdeationState = "thinking"       // agent turn in flight
	IdeationAwaitingReply IdeationState = "awaiting_reply" // agent asked, user must answer
	IdeationDone          IdeationState = "done"           // ticket created
	IdeationError         IdeationState = "error"          // turn or creation failed
)

// IdeationMessage is one transcript entry. Role is "user" or "agent".
type IdeationMessage struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// IdeationMode selects which agent prompt/turn discipline the session runs.
// Chat is the only mode: the guided one-question-at-a-time interview and its
// draft-approval park were retired with the promotion path they existed for
// (SC-4608). The type survives because the wire field and the persisted
// session still carry it.
type IdeationMode string

const (
	IdeationModeChat IdeationMode = "chat"
)

// IdeationStatus is the wire snapshot returned by all ideation routes.
type IdeationStatus struct {
	SessionID  string            `json:"session_id,omitempty"`
	Mode       IdeationMode      `json:"mode,omitempty"`
	State      IdeationState     `json:"state"`
	Transcript []IdeationMessage `json:"transcript,omitempty"`
	CreatedKey string            `json:"created_key,omitempty"`
	CreatedURL string            `json:"created_url,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// IdeationStartRequest is the wire request for ideation-start. Seed is the
// user's initial idea text. Restart abandons an active session first.
//
// A session's terminal action is creating a PM ticket, and only that: the
// evolve mode that rewrote an existing idea ticket in place went with the
// promotion path it served (SC-4608). An idea is promoted by removing its
// labels (idea-promote) and refined in the description editor.
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

// ideationSession is the engine's internal mutable state (guarded by engine mu).
type ideationSession struct {
	id              string
	mode            IdeationMode
	state           IdeationState
	transcript      []IdeationMessage
	resumeID        string
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
	// Store persists the session so a live chat survives a daemon restart —
	// notably the self-restart handover, which lands between turns exactly when
	// the user is composing a reply. nil keeps the engine memory-only.
	Store IdeationStore

	mu   sync.Mutex
	sess *ideationSession
}

// persistLocked writes the current session through to the store. Caller must
// hold mu. A store failure is logged, never fatal: losing durability must not
// break a running conversation.
func (e *IdeationEngine) persistLocked() {
	if e.Store == nil {
		return
	}
	var err error
	if e.sess == nil {
		err = e.Store.Clear()
	} else {
		err = e.Store.Save(e.sess.persist())
	}
	if err != nil {
		e.Logger.Warn().Err(err).Msg("persisting ideation session failed; it will not survive a restart")
	}
}

// commitLocked persists the session and releases the lock. Every mutation path
// ends here instead of a bare Unlock, so no state change escapes unsaved.
func (e *IdeationEngine) commitLocked() {
	e.persistLocked()
	e.mu.Unlock()
}

// Restore brings back a session saved by a previous process. It is called once
// at startup, before the engine serves any request, and ignores a session that
// finished or went stale (see PersistedIdeation.restorable).
func (e *IdeationEngine) Restore(p PersistedIdeation, now time.Time, maxAge time.Duration) bool {
	if !p.restorable(now, maxAge) {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sess = restoreSession(normalizeForRestore(p))
	e.persistLocked()
	return true
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
	e.commitLocked()

	go e.runTurn(sess.id, "", ideationPrompt(req.Seed))
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
	e.commitLocked()

	go e.runTurn(sessID, resumeID, req.Message)
	return snap, nil
}

// Status returns the current snapshot; State==IdeationNone when no session.
func (e *IdeationEngine) Status() IdeationStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshot()
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
		e.commitLocked()
		return
	}

	e.sess.resumeID = turn.ResumeID

	ticket, ticketFound, ticketErr := parseTicketBlock(turn.Reply)
	switch {
	case ticketFound && ticketErr == nil:
		// Chat mode keeps auto-creating the ticket the instant a valid block
		// is parsed — unchanged from HUM-152.
		stripped := strings.TrimSpace(ticketBlockRe.ReplaceAllString(turn.Reply, ""))
		if stripped != "" {
			e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: stripped, Time: time.Now()})
		}
		e.commitLocked()
		e.createTicket(sessID, ticket.Title, ticket.Description)
	case ticketFound:
		e.applyRepairOrError(sessID, turn.Reply, ideationRepairPrompt, "agent emitted a malformed ticket block")
	default:
		// Plain free text with no ticket marker — the normal question/answer
		// turn.
		e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: turn.Reply, Time: time.Now()})
		e.sess.state = IdeationAwaitingReply
		e.sess.repairAttempted = false
		e.commitLocked()
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
		e.commitLocked()
		go e.runTurn(sessID, resume, repairPrompt)
		return
	}
	e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: reply, Time: time.Now()})
	e.sess.state = IdeationError
	e.sess.errMsg = errMsg
	e.commitLocked()
}

// createTicket materializes the session's outcome: a new PM ticket. Run without
// holding mu so the network call cannot block Status()/Reply().
func (e *IdeationEngine) createTicket(sessID, title, description string) {
	if e.ResolveCreator == nil {
		e.mu.Lock()
		if e.sess != nil && e.sess.id == sessID {
			e.sess.state = IdeationError
			e.sess.errMsg = "no PM ticket creator configured"
		}
		e.commitLocked()
		return
	}

	creator, project, err := e.ResolveCreator()
	if err != nil {
		e.mu.Lock()
		if e.sess != nil && e.sess.id == sessID {
			e.sess.state = IdeationError
			e.sess.errMsg = err.Error()
		}
		e.commitLocked()
		return
	}

	created, err := createOwnedIssue(creator, &tracker.Issue{Project: project, Title: title, Description: description})

	e.mu.Lock()
	defer e.mu.Unlock()
	// Registered after the unlock so LIFO runs it first — the write happens
	// while the lock is still held, on every return path below.
	defer e.persistLocked()
	if e.sess == nil || e.sess.id != sessID {
		return
	}
	if err != nil {
		e.sess.state = IdeationError
		e.sess.errMsg = errors.WrapWithDetails(err, "creating PM ticket from ideation", "project", project).Error()
		return
	}
	if created == nil {
		// A provider must return the created issue on success, but a broken
		// one failing that contract must surface as an error, not a panic.
		e.sess.state = IdeationError
		e.sess.errMsg = "tracker returned no issue for the created ticket"
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

// CreateIdea delegates to IdeaCreator, which owns idea capture. The method
// survives only so this extraction commit changes no behaviour; it goes with
// the engine.
func (e *IdeationEngine) CreateIdea(title string) (key, url string, err error) {
	return (&IdeaCreator{ResolveCreator: e.ResolveCreator, Notify: e.Notify}).CreateIdea(title)
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
