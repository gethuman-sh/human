// Package agentstate is the durable working memory pipeline agents hand to one
// another. A stage records what it learned under a ticket scope and the next
// stage — or a fresh agent taking over a crashed one — reads it back instead of
// re-deriving it from nothing.
//
// It is deliberately separate from the [human:*] marker protocol
// (internal/marker): markers are the public, human-readable record on the
// ticket, while this is the verbose internal context that would pollute it.
// State never leaves the machine it is written on.
package agentstate

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"regexp"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// TimeFormat is fixed-width down to the nanosecond so lexical string comparison
// equals chronological ordering — the prune and the claim-staleness check both
// rely on that. RFC3339Nano cannot be used: it trims trailing zeros and so is
// not fixed width, which silently breaks ordered comparison.
const TimeFormat = "2006-01-02 15:04:05.000000000"

// MaxValueBytes caps a single entry. State is working memory that another agent
// reads back in full, not a file store: an agent that needs more than this
// should leave a path behind, not the payload.
const MaxValueBytes = 256 << 10

// DefaultClaimTTL is how long a stage claim stays live without a heartbeat.
// Past it the claim is considered abandoned and a fresh agent may take over —
// the recovery path for an agent that died mid-stage.
const DefaultClaimTTL = 15 * time.Minute

// DefaultRetention is how long entries survive without an update before the
// daemon's maintenance sweep prunes them.
const DefaultRetention = 14 * 24 * time.Hour

// Formats an entry's value may carry. JSON values are validated on write so a
// reader never has to defend against a half-written blob.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// ErrNotFound is returned by Get for a scope/name that was never written or has
// been deleted. Callers distinguish it with errors.Is.
var ErrNotFound = stderrors.New("state entry not found")

// ErrClaimHeld is returned by Claim when a live claim belongs to another agent
// and no takeover was requested.
var ErrClaimHeld = stderrors.New("stage claim held by another agent")

// namePattern keeps names to a dotted namespace so `list --prefix` stays a
// meaningful query and names never carry whitespace or wildcards.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Meta is the provenance recorded with every write: which agent wrote it and
// under which run. It answers "who left this here" when a successor takes over.
type Meta struct {
	Agent string
	RunID string
}

// Entry is one stored value.
type Entry struct {
	Scope     string    `json:"scope"`
	Name      string    `json:"name"`
	Value     string    `json:"value"`
	Format    string    `json:"format"`
	Agent     string    `json:"agent,omitempty"`
	RunID     string    `json:"run_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Claim is one agent's hold on a stage of a scope.
//
// TTL is stored with the claim because the holder — not whoever comes knocking
// later — knows how often it heartbeats. A short-running stage can declare a
// short TTL and be reclaimed quickly; a slow one declares a long TTL and is not
// stolen out from under it. Judging staleness by the challenger's TTL instead
// would mean a successor had to guess its predecessor's cadence.
type Claim struct {
	Scope       string        `json:"scope"`
	Stage       string        `json:"stage"`
	Agent       string        `json:"agent"`
	RunID       string        `json:"run_id,omitempty"`
	TTL         time.Duration `json:"ttl"`
	ClaimedAt   time.Time     `json:"claimed_at"`
	HeartbeatAt time.Time     `json:"heartbeat_at"`
	ReleasedAt  *time.Time    `json:"released_at,omitempty"`
}

// ClaimRequest asks to hold a stage. A zero TTL means DefaultClaimTTL, and the
// TTL applies to the claim being made — an existing holder's liveness is always
// judged by the TTL it declared itself. Takeover displaces even a live claim:
// the explicit override for when an operator knows the holder is gone before
// its heartbeat has expired.
type ClaimRequest struct {
	Scope    string
	Stage    string
	Meta     Meta
	TTL      time.Duration
	Takeover bool
}

// ClaimResult reports the outcome of a claim attempt. When a claim is granted
// over a previous holder, Displaced names it and InheritedKeys lists the state
// that holder left behind — the successor's starting point, so a crashed
// stage's work is inherited rather than redone.
type ClaimResult struct {
	Granted       bool     `json:"granted"`
	Claim         Claim    `json:"claim"`
	Displaced     *Claim   `json:"displaced,omitempty"`
	InheritedKeys []string `json:"inherited_keys,omitempty"`
}

// Store is the persistence seam. Commands accept this interface so their tests
// need no database, and an alternative backend never has to touch the CLI.
type Store interface {
	Set(ctx context.Context, scope, name, value, format string, meta Meta) (Entry, error)
	Get(ctx context.Context, scope, name string) (Entry, error)
	List(ctx context.Context, scope, prefix string) ([]Entry, error)
	Delete(ctx context.Context, scope, name string) (bool, error)
	DeletePrefix(ctx context.Context, scope, prefix string) (int, error)
	DeleteScope(ctx context.Context, scope string) (int, error)
	Incr(ctx context.Context, scope, name string, by int64, meta Meta) (int64, error)
	Claim(ctx context.Context, req ClaimRequest) (ClaimResult, error)
	Release(ctx context.Context, scope, stage, agent string) (bool, error)
	Claims(ctx context.Context, scope string) ([]Claim, error)
	Prune(ctx context.Context, cutoff time.Time) (int, error)
	Close() error
}

// NormalizeScope upper-cases and trims a scope so "sc-1200" and "SC-1200" are
// the same ticket, matching how tracker keys are written in practice.
func NormalizeScope(scope string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(scope))
	if s == "" {
		return "", errors.WithDetails("scope must not be empty")
	}
	return s, nil
}

// ValidateName rejects names that would make the namespace unqueryable.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return errors.WithDetails(
			"name must start alphanumeric and contain only letters, digits, dot, dash or underscore",
			"name", name,
		)
	}
	return nil
}

// ValidateStage applies the same grammar to a claim's stage name.
func ValidateStage(stage string) error {
	if !namePattern.MatchString(stage) {
		return errors.WithDetails("stage must be a simple name", "stage", stage)
	}
	return nil
}

// normalizeFormat defaults an empty format to text and validates JSON payloads
// at write time, so a reader can trust the format column.
func normalizeFormat(format, value string) (string, error) {
	switch format {
	case "", FormatText:
		return FormatText, nil
	case FormatJSON:
		if !json.Valid([]byte(value)) {
			return "", errors.WithDetails("value is not valid JSON", "format", format)
		}
		return FormatJSON, nil
	default:
		return "", errors.WithDetails("unknown format", "format", format)
	}
}

// validateValue enforces the size cap.
func validateValue(value string) error {
	if len(value) > MaxValueBytes {
		return errors.WithDetails(
			"value exceeds the size cap; store a pointer to the payload instead",
			"bytes", len(value), "max_bytes", MaxValueBytes,
		)
	}
	return nil
}
