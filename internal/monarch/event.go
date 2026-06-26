// Package monarch defines the team-level observability event model and the
// best-effort TCP transport a human daemon uses to stream what its swarm is
// working on. Events are identity-free: they describe work (ticket, repo,
// state) and an opaque daemon instance, never a person.
package monarch

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// EventType enumerates the MVP lifecycle events a daemon streams to monarch.
type EventType string

const (
	EventAgentStart EventType = "agent.start"
	EventAgentStop  EventType = "agent.stop"
	EventTokensUsed EventType = "tokens.used"
	// EventHeartbeat is sent periodically by an otherwise-idle daemon so monarch
	// can show it is connected even when no agent is running. It carries no
	// agent/work fields — only the daemon id and timestamp.
	EventHeartbeat EventType = "daemon.heartbeat"
)

// State is the work-board state a daemon reports for an agent.
type State string

const (
	StatePlanning State = "planning"
	StateCoding   State = "coding"
	StateBlocked  State = "blocked"
	StateIdle     State = "idle"
	StateStopped  State = "stopped"
)

// TokenPayload carries cumulative token counts for a session at emission time.
type TokenPayload struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	CacheCreate  int `json:"cache_create,omitempty"`
	CacheRead    int `json:"cache_read,omitempty"`
}

// Event is the newline-delimited JSON object a daemon writes to monarch. It is
// identity-free by construction: DaemonID is an opaque stable instance id;
// there is no name/email/host field anywhere in this struct.
type Event struct {
	Type      EventType     `json:"type"`
	Team      string        `json:"team,omitempty"`
	DaemonID  string        `json:"daemon_id"`
	AgentID   string        `json:"agent_id,omitempty"`
	TicketKey string        `json:"ticket_key,omitempty"`
	Repo      string        `json:"repo,omitempty"`
	Branch    string        `json:"branch,omitempty"`
	State     State         `json:"state,omitempty"`
	TS        time.Time     `json:"ts"`
	Payload   *TokenPayload `json:"payload,omitempty"`
}

// daemonIDBytes is small on purpose: the id only needs to be locally unique and
// opaque, and a short suffix keeps the work board readable (e.g. "daemon-7f3a").
const daemonIDBytes = 4

// NewDaemonID returns an opaque "daemon-<hex>" id from random bytes. The id is
// never derived from any person/host identity, satisfying the privacy invariant.
func NewDaemonID() (string, error) {
	b := make([]byte, daemonIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", errors.WrapWithDetails(err, "generate daemon id")
	}
	return "daemon-" + hex.EncodeToString(b), nil
}
