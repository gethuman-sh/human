package daemon

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/ideadraft"
	"github.com/gethuman-sh/human/internal/tracker"
)

// DescEditState is the lifecycle state of one Product-Backlog description-edit
// chat session.
type DescEditState string

const (
	DescEditNone          DescEditState = "none"
	DescEditThinking      DescEditState = "thinking"
	DescEditAwaitingReply DescEditState = "awaiting_reply"
	DescEditApplied       DescEditState = "applied" // terminal for this session — reopen starts a fresh one
	DescEditError         DescEditState = "error"
)

// DescEditMessage is one transcript entry. Role is "user", "agent", or
// "system" (the latter only for the Apply confirmation line).
type DescEditMessage struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// DescEditStatus is the wire snapshot returned by every description-edit route.
type DescEditStatus struct {
	SessionID  string            `json:"session_id,omitempty"`
	Key        string            `json:"key,omitempty"`
	State      DescEditState     `json:"state"`
	Transcript []DescEditMessage `json:"transcript,omitempty"`
	// Proposal is the latest agent-proposed replacement description text.
	// Empty means no live proposal — the left pane shows the saved description.
	Proposal   string `json:"proposal,omitempty"`
	AppliedURL string `json:"applied_url,omitempty"`
	Error      string `json:"error,omitempty"`
}

// DescEditStartRequest starts (or re-attaches to) the chat for one ticket.
// CurrentDescription seeds the agent's context — the caller fetches it via
// the existing GetIssueDetail route first (some trackers' list fetches omit
// descriptions).
type DescEditStartRequest struct {
	Key                string `json:"key"`
	CurrentDescription string `json:"current_description"`
	Restart            bool   `json:"restart,omitempty"`
	// Promoted marks the session opened by dragging an idea onto Product
	// Backlog. It widens the chat's remit — the guided interview this replaced
	// existed to push back, and a copy-editor that may not discuss scope
	// replaces none of that (SC-4608).
	Promoted bool `json:"promoted,omitempty"`
}

// DescEditReplyRequest sends the user's chat message into a running session.
type DescEditReplyRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// DescEditApplyRequest writes the session's current proposal to the tracker.
type DescEditApplyRequest struct {
	SessionID string `json:"session_id"`
}

// DescEditDiscardRequest ends a session without writing anything to the
// tracker — the modal's close-without-apply path (AC6).
type DescEditDiscardRequest struct {
	SessionID string `json:"session_id"`
}

// ChatTurn is one completed headless agent turn.
type ChatTurn struct {
	Reply    string // agent's text output for this turn
	ResumeID string // provider session id to resume the next turn
}

// DescEditRunner runs one headless agent turn for the description-edit chat.
// resumeID is empty on the first turn; session continuity across turns rides
// on the provider's own resume store, so the daemon holds no conversation
// state beyond the id. Implementations must be safe for sequential reuse.
type DescEditRunner interface {
	Run(ctx context.Context, resumeID, prompt string) (ChatTurn, error)
}

type descEditSession struct {
	id                 string
	key                string
	currentDescription string
	state              DescEditState
	transcript         []DescEditMessage
	resumeID           string
	proposal           string
	appliedURL         string
	errMsg             string
	repairAttempted    bool
	promoted           bool
}

// PMEditorResolver resolves the PM-role tracker's Editor. The description
// editor is its owner — Apply is the only agent-driven path that writes a
// ticket's description — and the promote route resolves the same way for the
// label edit.
type PMEditorResolver func() (tracker.Editor, error)

// DescEditEngine owns the single active Product-Backlog description-edit
// session. All exported methods are safe for concurrent use. Sessions are NOT
// persisted across a daemon restart: nothing is ever written to the tracker
// before Apply, so losing an in-progress, unsaved chat is low-cost.
type DescEditEngine struct {
	Runner        DescEditRunner
	ResolveEditor PMEditorResolver // role-resolved PM tracker.Editor; the sole write path
	// ResolveCommenter resolves the PM tracker's commenter used to record that
	// an applied description is now the human's words. nil disables the record,
	// which costs the guard nothing it had before.
	ResolveCommenter func() (tracker.Commenter, error)
	TurnTimeout      time.Duration // defaults to 5 * time.Minute when zero
	Logger           zerolog.Logger

	mu   sync.Mutex
	sess *descEditSession
}

// descEditSessionSeq guarantees unique session IDs even when Start is called
// twice within the same clock tick — on some hosts UnixNano() resolves no
// finer than ~1µs, so two back-to-back calls collide most of the time on a
// timestamp alone.
var descEditSessionSeq int64

func newDescEditSessionID() string {
	return fmt.Sprintf("descedit-%d-%d", time.Now().UnixNano(), atomic.AddInt64(&descEditSessionSeq, 1))
}

func (e *DescEditEngine) turnTimeout() time.Duration {
	if e.TurnTimeout > 0 {
		return e.TurnTimeout
	}
	return 5 * time.Minute
}

func (e *DescEditEngine) snapshot() DescEditStatus {
	if e.sess == nil {
		return DescEditStatus{State: DescEditNone}
	}
	s := e.sess
	return DescEditStatus{
		SessionID:  s.id,
		Key:        s.key,
		State:      s.state,
		Transcript: append([]DescEditMessage(nil), s.transcript...),
		Proposal:   s.proposal,
		AppliedURL: s.appliedURL,
		Error:      s.errMsg,
	}
}

// Start begins a new session, or re-attaches to an active one for the SAME
// key. Closing the description-edit modal DOES end the session (Discard,
// called from the modal's close path) — so in normal
// use this reattach only ever fires within a single still-open modal
// instance (e.g. a retried Start racing its own in-flight call), never across
// a close/reopen. A different key or Restart:true always starts fresh — no
// LLM turn fires here; the session opens idle (see Architecture Decisions).
func (e *DescEditEngine) Start(req DescEditStartRequest) (DescEditStatus, error) {
	if strings.TrimSpace(req.Key) == "" {
		return DescEditStatus{}, errors.WithDetails("description-edit key must not be empty")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess != nil && e.sess.key == req.Key && !req.Restart &&
		(e.sess.state == DescEditThinking || e.sess.state == DescEditAwaitingReply) {
		return e.snapshot(), nil
	}
	e.sess = &descEditSession{
		id:                 newDescEditSessionID(),
		key:                req.Key,
		currentDescription: req.CurrentDescription,
		state:              DescEditAwaitingReply,
		promoted:           req.Promoted,
	}
	return e.snapshot(), nil
}

// Reply feeds the user's chat message into the running session. The first
// real turn (resumeID empty) carries the full scoped system prompt plus the
// current description; later turns rely on --resume for continuity and send
// only the plain message.
func (e *DescEditEngine) Reply(req DescEditReplyRequest) (DescEditStatus, error) {
	if strings.TrimSpace(req.Message) == "" {
		return DescEditStatus{}, errors.WithDetails("description-edit reply message must not be empty")
	}
	e.mu.Lock()
	if e.sess == nil || req.SessionID != e.sess.id {
		e.mu.Unlock()
		return DescEditStatus{}, errors.WithDetails("no matching description-edit session", "session", req.SessionID)
	}
	if e.sess.state != DescEditAwaitingReply {
		state := e.sess.state
		e.mu.Unlock()
		return DescEditStatus{}, errors.WithDetails("description-edit session is not awaiting a reply", "state", string(state))
	}
	e.sess.transcript = append(e.sess.transcript, DescEditMessage{Role: "user", Text: req.Message, Time: time.Now()})
	e.sess.state = DescEditThinking
	resumeID := e.sess.resumeID
	sessID := e.sess.id
	currentDescription := e.sess.currentDescription
	promoted := e.sess.promoted
	snap := e.snapshot()
	e.mu.Unlock()

	prompt := req.Message
	if resumeID == "" {
		prompt = descEditSystemPrompt(currentDescription, promoted) + "\n\nUser: " + req.Message
	}
	go e.runTurn(sessID, resumeID, prompt)
	return snap, nil
}

// Status returns the current snapshot; State==DescEditNone when no session.
func (e *DescEditEngine) Status() DescEditStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshot()
}

// Apply writes the session's current proposal to the tracker via the
// role-resolved PM Editor, touching ONLY the Description field — Title and
// labels are left nil, so this can never drift into editing other fields
// (the AC's scope guarantee). Idempotent: re-calling after a successful
// apply returns the same terminal snapshot without re-writing.
func (e *DescEditEngine) Apply(req DescEditApplyRequest) (DescEditStatus, error) {
	e.mu.Lock()
	if e.sess == nil || req.SessionID != e.sess.id {
		e.mu.Unlock()
		return DescEditStatus{}, errors.WithDetails("no matching description-edit session", "session", req.SessionID)
	}
	if e.sess.state == DescEditApplied {
		snap := e.snapshot()
		e.mu.Unlock()
		return snap, nil
	}
	if strings.TrimSpace(e.sess.proposal) == "" {
		state := e.sess.state
		e.mu.Unlock()
		return DescEditStatus{}, errors.WithDetails("no proposed rewrite to apply yet", "state", string(state))
	}
	key := e.sess.key
	proposal := e.sess.proposal
	sessID := e.sess.id
	e.mu.Unlock()

	if e.ResolveEditor == nil {
		return DescEditStatus{}, errors.WithDetails("no PM ticket editor configured")
	}
	editor, err := e.ResolveEditor()
	if err != nil {
		return DescEditStatus{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	updated, err := editor.EditIssue(ctx, key, tracker.EditOptions{Description: &proposal})
	if err != nil {
		return DescEditStatus{}, errors.WrapWithDetails(err, "saving description edit", "key", key)
	}
	if updated == nil {
		return DescEditStatus{}, errors.WithDetails("tracker returned no issue for the description edit", "key", key)
	}

	// The applied text is the user's, whether they wrote every word or accepted
	// the machine's draft unchanged. Pinning it as human-authored is what stops
	// a redraft still in flight — a debounce armed before the labels came off —
	// from writing over the edit the user just made.
	e.recordHumanFingerprint(key, proposal)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess == nil || e.sess.id != sessID {
		return DescEditStatus{State: DescEditApplied, Key: key, AppliedURL: updated.URL}, nil
	}
	e.sess.state = DescEditApplied
	e.sess.appliedURL = updated.URL
	e.sess.transcript = append(e.sess.transcript, DescEditMessage{Role: "system", Text: "Description saved.", Time: time.Now()})
	return e.snapshot(), nil
}

// Discard ends the session named by SessionID without touching the tracker —
// the modal's close-without-apply path (AC6: "closing the modal discards the
// proposed (unsaved) rewrite"). Ending the session, not just clearing its
// proposal, is what matters: Start's same-key reattach (above) checks
// e.sess != nil, so dropping the session is what makes a later Start for the
// same key start genuinely fresh instead of reattaching to a stale
// AwaitingReply session that still carries the discarded proposal and chat
// history. A SessionID that no longer names the active session (already
// applied, already discarded, or superseded by a fresh Start for a different
// ticket) is a safe no-op — the caller fires this best-effort from the
// modal's close handler without waiting on network ordering.
func (e *DescEditEngine) Discard(req DescEditDiscardRequest) DescEditStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess != nil && e.sess.id == req.SessionID {
		e.sess = nil
	}
	return e.snapshot()
}

// runTurn executes one headless agent turn and applies its result. Runs in
// its own goroutine so Reply returns immediately: turns are asynchronous and
// the client polls for status rather than holding the connection open.
func (e *DescEditEngine) runTurn(sessID, resumeID, prompt string) {
	ctx, cancel := context.WithTimeout(context.Background(), e.turnTimeout())
	defer cancel()
	turn, err := e.Runner.Run(ctx, resumeID, prompt)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sess == nil || e.sess.id != sessID {
		return
	}
	if err != nil {
		e.Logger.Error().Fields(errors.AllDetails(err)).Msg(errors.CauseChain(err))
		e.sess.state = DescEditError
		e.sess.errMsg = err.Error()
		return
	}
	e.sess.resumeID = turn.ResumeID

	proposal, stripped, found, perr := parseDescProposalBlock(turn.Reply)
	switch {
	case found && perr == nil:
		if stripped != "" {
			e.sess.transcript = append(e.sess.transcript, DescEditMessage{Role: "agent", Text: stripped, Time: time.Now()})
		}
		e.sess.proposal = proposal
		e.sess.state = DescEditAwaitingReply
		e.sess.repairAttempted = false
	case found:
		if !e.sess.repairAttempted {
			e.sess.repairAttempted = true
			resume := e.sess.resumeID
			e.sess.state = DescEditThinking
			go e.runTurn(sessID, resume, descEditRepairPrompt)
			return
		}
		e.sess.transcript = append(e.sess.transcript, DescEditMessage{Role: "agent", Text: turn.Reply, Time: time.Now()})
		e.sess.state = DescEditError
		e.sess.errMsg = "agent emitted a malformed description-proposal block"
	default:
		e.sess.transcript = append(e.sess.transcript, DescEditMessage{Role: "agent", Text: turn.Reply, Time: time.Now()})
		e.sess.state = DescEditAwaitingReply
		e.sess.repairAttempted = false
	}
}

const descProposalMarker = "[human:description-proposal]"

var descProposalBlockRe = regexp.MustCompile(
	`(?s)\[human:description-proposal\]\s*` + "```(?:markdown|md)?\\s*(.*?)```")

// parseDescProposalBlock extracts the latest proposed replacement description
// from an agent reply. found is true when the marker is present; err reports
// a malformed payload (marker present but no fenced block, or an empty
// proposal). stripped is the reply with the marker block removed and
// trimmed, for display in the transcript (an empty stripped is normal when
// the whole reply IS the proposal).
func parseDescProposalBlock(reply string) (proposal, stripped string, found bool, err error) {
	if !strings.Contains(reply, descProposalMarker) {
		return "", reply, false, nil
	}
	m := descProposalBlockRe.FindStringSubmatch(reply)
	if m == nil {
		return "", reply, true, errors.WithDetails("description-proposal marker present but no fenced block found")
	}
	proposal = strings.TrimSpace(m[1])
	if proposal == "" {
		return "", reply, true, errors.WithDetails("description-proposal block is empty")
	}
	stripped = strings.TrimSpace(descProposalBlockRe.ReplaceAllString(reply, ""))
	return proposal, stripped, true, nil
}

// descEditRepairPrompt is the one-shot corrective turn for a malformed marker
// block. Sent via --resume, so the agent retains full conversation context.
const descEditRepairPrompt = "Your previous message contained the [human:description-proposal] marker but the fenced " +
	"block was missing or empty. Re-emit ONLY the line [human:description-proposal] followed by a fenced ```markdown " +
	"code block containing the complete replacement description text — no other text."

// recordHumanFingerprint posts the provenance record that says the description
// is now human-authored. Best effort on purpose: a failed comment must never
// turn a saved description into a failed apply — the user's words are already
// on the ticket, and the worst case is a later redraft standing down for a
// different reason (an unrecognised description) rather than this one.
func (e *DescEditEngine) recordHumanFingerprint(key, text string) {
	if e.ResolveCommenter == nil {
		return
	}
	commenter, err := e.ResolveCommenter()
	if err != nil || commenter == nil {
		e.Logger.Error().Err(err).Str("key", key).
			Msg("description edit applied but its human-authored record could not be posted")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := postMarker(ctx, commenter, key, ideadraft.HumanRecord(text), ideadraft.FieldOrder...); err != nil {
		e.Logger.Error().Err(err).Str("key", key).
			Msg("description edit applied but its human-authored record could not be posted")
	}
}

// descEditSystemPrompt builds the chat's discipline: description text ONLY,
// never other fields, explicit user Apply. A promoted card widens the remit —
// the guided interview promotion used to open existed to push back, and a
// copy-editor that may not discuss scope replaces none of that.
func descEditSystemPrompt(currentDescription string, promoted bool) string {
	var b strings.Builder
	b.WriteString("You are a description-editing assistant helping a product owner refine ONE ticket's description ")
	b.WriteString("text through conversation. You may read the repository with read-only tools for context.\n\n")
	if promoted {
		b.WriteString("This ticket has just been promoted from a raw idea, so your job is wider than copy-editing. ")
		b.WriteString("You may — and should — challenge the premise: whether this is worth doing, what is in and out ")
		b.WriteString("of scope, and what the smallest version worth shipping is. Push back where the description ")
		b.WriteString("asserts something the repository does not support.\n")
		b.WriteString("The description carries inline `[TBA: <question>]` markers where a background drafter refused ")
		b.WriteString("to guess. Work through them with the user, one at a time, and replace each with the user's own ")
		b.WriteString("answer in the sentence it sits in. NEVER answer a [TBA:] yourself, and never delete one the ")
		b.WriteString("user has not answered — an invented answer is the exact failure the marker exists to prevent. ")
		b.WriteString("Say plainly when none are left.\n")
		b.WriteString("You still propose DESCRIPTION text only: the title, labels, status and every other field are ")
		b.WriteString("out of this chat's reach, and Apply writes the description and nothing else.\n\n")
	} else {
		b.WriteString("Your ONLY job is proposing rewrites of the description text below. Never suggest or discuss ")
		b.WriteString("changing the title, acceptance criteria structure, labels, status, or any other ticket field — if ")
		b.WriteString("asked, say that is out of scope for this chat.\n\n")
	}
	b.WriteString("Discuss phrasing, structure, and clarity in plain chat replies. When you have a concrete rewrite ")
	b.WriteString("ready for the user's review, output the line `[human:description-proposal]` followed by a fenced ")
	b.WriteString("```markdown code block containing the COMPLETE replacement description text (the full text, not a ")
	b.WriteString("diff or a summary of changes). Only emit the marker when the proposal is genuinely ready for ")
	b.WriteString("review; ordinary clarifying chat must not include it. Do not create or modify anything yourself — ")
	b.WriteString("the user applies the change explicitly.\n\n")
	b.WriteString("Current description:\n<description>\n" + currentDescription + "\n</description>")
	return b.String()
}
