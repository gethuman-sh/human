package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"time"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/monarch"
)

// MonarchSink is the subset of the monarch sender the daemon depends on.
// Extracted as an interface so the emit choke points can be tested with a fake
// that captures events, without standing up a real TCP sender.
type MonarchSink interface {
	Send(monarch.Event)
}

// ticketKeyRe matches an issue key like HUM-59 or SC-110 anywhere in a prompt.
var ticketKeyRe = regexp.MustCompile(`[A-Z][A-Z0-9]+-\d+`)

// emitMonarch streams an identity-free telemetry event for a hook lifecycle
// event. It is a no-op unless monarch is enabled, so it is safe to call from the
// hook choke point regardless of whether the in-memory hook buffer is on.
func (s *Server) emitMonarch(evt hookevents.Event) {
	if s.MonarchSink == nil || s.DaemonID == "" {
		return
	}
	// Burn is independent of the work-state mapping: token counts arrive on
	// Stop/SessionEnd, which are not all work-board states, so emit them first.
	s.emitTokens(evt)

	et, state, ok := monarchTypeForHook(evt.EventName, evt.NotificationType)
	if !ok {
		return
	}
	e := monarch.Event{
		Type:     et,
		DaemonID: s.DaemonID,
		AgentID:  hashAgentID(evt.AgentName, evt.SessionID),
		State:    state,
		TS:       time.Now().UTC(),
	}
	s.enrichFromAgentMeta(&e, evt.AgentName, evt.Cwd)
	s.MonarchSink.Send(e)
}

// emitTokens streams a tokens.used event carrying the session's cumulative token
// counts (parsed client-side from the transcript). It is a no-op when no counts
// are present, so events without burn data never create empty burn rows.
func (s *Server) emitTokens(evt hookevents.Event) {
	total := evt.InputTokens + evt.OutputTokens + evt.CacheCreate + evt.CacheRead
	if total == 0 {
		return
	}
	e := monarch.Event{
		Type:     monarch.EventTokensUsed,
		DaemonID: s.DaemonID,
		AgentID:  hashAgentID(evt.AgentName, evt.SessionID),
		TS:       time.Now().UTC(),
		Payload: &monarch.TokenPayload{
			InputTokens:  evt.InputTokens,
			OutputTokens: evt.OutputTokens,
			CacheCreate:  evt.CacheCreate,
			CacheRead:    evt.CacheRead,
		},
	}
	s.enrichFromAgentMeta(&e, evt.AgentName, evt.Cwd)
	s.MonarchSink.Send(e)
}

// emitMonarchStop streams an agent.stop when an agent is decommissioned, so the
// work board drops it promptly even without a SessionEnd hook.
func (s *Server) emitMonarchStop(name string) {
	if s.MonarchSink == nil || s.DaemonID == "" {
		return
	}
	e := monarch.Event{
		Type:     monarch.EventAgentStop,
		DaemonID: s.DaemonID,
		AgentID:  hashAgentID(name, ""),
		State:    monarch.StateStopped,
		TS:       time.Now().UTC(),
	}
	s.enrichFromAgentMeta(&e, name, "")
	s.MonarchSink.Send(e)
}

// monarchTypeForHook maps a hook event to a monarch (type, state). The boolean
// is false for events monarch does not track.
func monarchTypeForHook(eventName, notificationType string) (monarch.EventType, monarch.State, bool) {
	switch eventName {
	case "SessionStart":
		return monarch.EventAgentStart, monarch.StatePlanning, true
	case "SessionEnd":
		return monarch.EventAgentStop, monarch.StateStopped, true
	case "PreToolUse", "PostToolUse", "PostToolUseFailure":
		return monarch.EventAgentStart, monarch.StateCoding, true
	case "PermissionRequest":
		return monarch.EventAgentStart, monarch.StateBlocked, true
	case "Notification":
		if notificationType == "permission_prompt" {
			return monarch.EventAgentStart, monarch.StateBlocked, true
		}
	}
	return "", "", false
}

// hashAgentID returns an opaque, stable "agent-<8hex>" id derived from the
// tool-assigned agent name and session id. It is identity-free: agent names are
// tool-assigned (e.g. human-agent-*), never a person. Empty input yields empty.
func hashAgentID(agentName, sessionID string) string {
	if agentName == "" && sessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(agentName + "|" + sessionID))
	return "agent-" + hex.EncodeToString(sum[:])[:8]
}

// enrichFromAgentMeta fills the work-board fields. Repo defaults to the base
// name of cwd; when an agent name is present and a metadata reader is injected,
// the persisted metadata provides a better cwd and a ticket key parsed from the
// launch prompt. The reader is injected (rather than importing internal/agent)
// to avoid an agent->daemon import cycle.
func (s *Server) enrichFromAgentMeta(e *monarch.Event, agentName, cwd string) {
	if cwd != "" {
		e.Repo = filepath.Base(cwd)
	}
	if agentName == "" || s.AgentMetaReader == nil {
		return
	}
	metaCwd, prompt, ok := s.AgentMetaReader(agentName)
	if !ok {
		return
	}
	if metaCwd != "" {
		e.Repo = filepath.Base(metaCwd)
	}
	e.TicketKey = parseTicketKey(prompt)
}

// parseTicketKey extracts the first issue key (e.g. HUM-59) from a prompt, or "".
func parseTicketKey(prompt string) string {
	return ticketKeyRe.FindString(prompt)
}
