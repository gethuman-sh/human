// Package audit records a structured, queryable trail of every mutating action
// an AI agent takes against issue trackers through the daemon. Each action is
// captured as a CloudEvents 1.0 envelope so the trail is machine-queryable and
// tool-friendly rather than ad-hoc free text.
package audit

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// SpecVersion is the CloudEvents specification version every emitted envelope
// declares. Pinned to the core 1.0 attributes the trail relies on.
const SpecVersion = "1.0"

// Outcome is the result of a mutating tracker action.
type Outcome string

const (
	// OutcomeSuccess means the command executed and exited 0.
	OutcomeSuccess Outcome = "success"
	// OutcomeFailure means the command executed and exited non-zero.
	OutcomeFailure Outcome = "failure"
	// OutcomeDenied means the action was intercepted as destructive and the
	// human reviewer rejected (or let time out) the confirmation.
	OutcomeDenied Outcome = "denied"
)

// Actor identifies which configured tracker credential performed the action.
// This is the CloudEvents source decomposed into queryable parts.
type Actor struct {
	TrackerKind string `json:"tracker_kind"`
	TrackerName string `json:"tracker_name,omitempty"`
	User        string `json:"user,omitempty"`
}

// Resource identifies the ticket or project the action targeted.
type Resource struct {
	Key     string `json:"key,omitempty"`
	Project string `json:"project,omitempty"`
}

// DecisionContext captures the model id/version, inputs, and reasoning exactly
// as they existed at decision time. Agent decisions are non-deterministic: a
// past decision cannot be reconstructed by replaying the same prompt against
// the model later, so the inputs and rationale must be captured here at
// emission time or they are lost forever.
type DecisionContext struct {
	ModelID      string `json:"model_id,omitempty"`
	ModelVersion string `json:"model_version,omitempty"`
	Inputs       string `json:"inputs,omitempty"`
	Rationale    string `json:"rationale,omitempty"`
}

// Data is the decomposed payload carried in the CloudEvents data attribute.
type Data struct {
	Operation string          `json:"operation"`
	Actor     Actor           `json:"actor"`
	Resource  Resource        `json:"resource"`
	Outcome   Outcome         `json:"outcome"`
	Args      []string        `json:"args,omitempty"`
	Decision  DecisionContext `json:"decision"`
}

// Event is the CloudEvents 1.0 envelope persisted and surfaced to "human audit".
type Event struct {
	SpecVersion     string    `json:"specversion"`
	ID              string    `json:"id"`
	Source          string    `json:"source"`
	Type            string    `json:"type"`
	Subject         string    `json:"subject,omitempty"`
	Time            time.Time `json:"time"`
	DataContentType string    `json:"datacontenttype"`
	Data            Data      `json:"data"`
}

// idBytes is the number of random bytes hex-encoded into an event id. 16 bytes
// yields a 32-char hex string — enough entropy for a unique CloudEvents id
// (the spec only requires uniqueness within source scope, not UUID format).
// Mirrors internal/daemon/token.go's crypto/rand pattern so no UUID dependency
// is pulled in.
const idBytes = 16

// newID returns a cryptographically random 32-char hex event id.
func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", errors.WrapWithDetails(err, "generate audit event id")
	}
	return hex.EncodeToString(b), nil
}

// buildEventWithID assembles a complete CloudEvents envelope from a decomposed
// data payload and a caller-supplied id. Tests use the explicit id to get a
// deterministic envelope; production passes a freshly generated newID().
func buildEventWithID(id string, now time.Time, d Data) Event {
	source := "human/daemon/" + d.Actor.TrackerKind
	if d.Actor.TrackerName != "" {
		source += "/" + d.Actor.TrackerName
	}

	// Prefer the concrete ticket key as the subject; fall back to the project
	// for create operations where no key exists yet.
	subject := d.Resource.Key
	if subject == "" {
		subject = d.Resource.Project
	}

	return Event{
		SpecVersion:     SpecVersion,
		ID:              id,
		Source:          source,
		Type:            "sh.human.tracker." + d.Operation,
		Subject:         subject,
		Time:            now.UTC(),
		DataContentType: "application/json",
		Data:            d,
	}
}
