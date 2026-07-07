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

// IdeationStatus is the wire snapshot returned by all three ideation routes.
type IdeationStatus struct {
	SessionID  string            `json:"session_id,omitempty"`
	State      IdeationState     `json:"state"`
	Transcript []IdeationMessage `json:"transcript,omitempty"`
	CreatedKey string            `json:"created_key,omitempty"`
	CreatedURL string            `json:"created_url,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// IdeationStartRequest is the wire request for ideation-start. Seed is the
// user's initial idea text. Restart abandons an active session first.
type IdeationStartRequest struct {
	Seed    string `json:"seed"`
	Restart bool   `json:"restart,omitempty"`
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

// PMCreatorResolver resolves the single PM-role tracker Creator and its first
// configured project, mirroring the role-based resolution of resolvePMCommenter.
type PMCreatorResolver func() (creator tracker.Creator, project string, err error)

// ideationSession is the engine's internal mutable state (guarded by engine mu).
type ideationSession struct {
	id              string
	state           IdeationState
	transcript      []IdeationMessage
	resumeID        string
	createdKey      string
	createdURL      string
	errMsg          string
	repairAttempted bool // one corrective turn allowed per malformed ticket block
}

// IdeationEngine owns the single board ideation session. All exported methods
// are safe for concurrent use.
type IdeationEngine struct {
	Runner         IdeationRunner
	ResolveCreator PMCreatorResolver
	Notify         func() // pokes the subscribe loop after ticket creation; nil ok
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

	now := time.Now()
	sess := &ideationSession{
		id:    fmt.Sprintf("ideation-%d", now.UnixNano()),
		state: IdeationThinking,
		transcript: []IdeationMessage{
			{Role: "user", Text: req.Seed, Time: now},
		},
	}
	e.sess = sess
	snap := e.snapshot()
	e.mu.Unlock()

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

	ticket, found, parseErr := parseTicketBlock(turn.Reply)
	switch {
	case !found:
		// A fresh question round earns a fresh repair budget: only a
		// malformed *ticket* block should ever count against the one-shot
		// repair allowance.
		e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: turn.Reply, Time: time.Now()})
		e.sess.state = IdeationAwaitingReply
		e.sess.repairAttempted = false
		e.mu.Unlock()
	case parseErr == nil:
		stripped := ticketBlockRe.ReplaceAllString(turn.Reply, "")
		stripped = strings.TrimSpace(stripped)
		if stripped != "" {
			e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: stripped, Time: time.Now()})
		}
		e.mu.Unlock()
		e.createTicket(sessID, ticket.Title, ticket.Description)
	default:
		if !e.sess.repairAttempted {
			e.sess.repairAttempted = true
			resume := e.sess.resumeID
			e.sess.state = IdeationThinking
			e.mu.Unlock()
			go e.runTurn(sessID, resume, ideationRepairPrompt)
			return
		}
		e.sess.transcript = append(e.sess.transcript, IdeationMessage{Role: "agent", Text: turn.Reply, Time: time.Now()})
		e.sess.state = IdeationError
		e.sess.errMsg = "agent emitted a malformed ticket block"
		e.mu.Unlock()
	}
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
