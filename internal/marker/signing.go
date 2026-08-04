package marker

import (
	"context"
	"strings"

	"github.com/gethuman-sh/human/internal/tracker"
)

// MachineField and BuildField are the two provenance fields every machine-written
// marker carries: which machine posted the record and which build that machine
// ran. They live in the parsed field block (before the body), so
// ParseBody(body).Fields[MachineField] answers "which machine wrote this" on
// every marker shape — provenance that can be queried, not a trailing line that
// can only be eyeballed.
const (
	MachineField = "machine"
	BuildField   = "build"
)

// Sign inserts machine/build provenance fields into a machine-written marker
// body and returns the signed body. It is pure and idempotent:
//
//   - A body that is not a [human:*] marker (a plain discussion comment) is
//     returned unchanged, so free-form and product text is never signed.
//   - A body that already carries a machine: field is returned unchanged, so a
//     re-post or a double-wrap during migration cannot double-stamp.
//   - Empty machine and build are omitted; a body with neither to add is
//     returned unchanged, matching the old StampDaemon empty-id no-op so an
//     un-provisioned daemon or an unstamped build still posts a valid marker.
//
// The fields are inserted at the END of the marker's field block by line
// surgery rather than a Render round-trip, so the existing field order
// (engineering/branch/commits on a handoff) is preserved.
func Sign(body, machine, build string) string {
	m, ok := ParseBody(body)
	if !ok {
		return body
	}
	if strings.TrimSpace(m.Fields[MachineField]) != "" {
		return body
	}
	var sig []string
	if machine = strings.TrimSpace(machine); machine != "" {
		sig = append(sig, MachineField+": "+machine)
	}
	if build = strings.TrimSpace(build); build != "" {
		sig = append(sig, BuildField+": "+build)
	}
	if len(sig) == 0 {
		return body
	}
	return insertFieldLines(body, sig)
}

// insertFieldLines splices sigLines in after the last line of a marker's field
// block — the contiguous run of key: value / continuation lines following the
// header, up to the first blank line or the start of the body. It mirrors
// ParseBody's field-scanning so the fields land exactly where ParseBody will
// read them back as fields rather than body.
func insertFieldLines(body string, sigLines []string) string {
	lines := strings.Split(body, "\n")
	headerIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if headerPattern.MatchString(strings.TrimSpace(line)) {
			headerIdx = i
		}
		break
	}
	if headerIdx == -1 {
		return body
	}
	insertAt := len(lines)
	haveField := false
	for i := headerIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			insertAt = i
			break
		}
		if fieldPattern.MatchString(line) {
			haveField = true
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && haveField {
			continue
		}
		// First non-field, non-continuation line without a preceding blank line:
		// the body starts here, so the signature must go before it (ParseBody's
		// tolerant reading treats this line as the body start too).
		insertAt = i
		break
	}
	out := make([]string, 0, len(lines)+len(sigLines))
	out = append(out, lines[:insertAt]...)
	out = append(out, sigLines...)
	out = append(out, lines[insertAt:]...)
	return strings.Join(out, "\n")
}

// SigningProvider wraps a tracker.Provider so every marker body posted through
// AddComment is signed with the machine and build it was injected with. It is
// the single choke point that replaces the scattered per-writer StampDaemon
// calls: a new marker write path inherits provenance without a stamping call.
// All non-comment operations delegate verbatim via the embedded interface.
type SigningProvider struct {
	tracker.Provider
	machine string
	build   string
}

// NewSigningProvider returns inner wrapped so AddComment signs marker bodies.
// Empty machine/build make Sign a pass-through, so wrapping is always safe.
func NewSigningProvider(inner tracker.Provider, machine, build string) tracker.Provider {
	return &SigningProvider{Provider: inner, machine: machine, build: build}
}

func (s *SigningProvider) AddComment(ctx context.Context, key, body string) (*tracker.Comment, error) {
	return s.Provider.AddComment(ctx, key, Sign(body, s.machine, s.build))
}

// AssignToReporter re-exposes an OPTIONAL capability of the wrapped provider.
//
// Embedding tracker.Provider promotes only the methods that interface declares,
// so a capability callers reach by type assertion — one deliberately kept off
// Provider so backends without it need not fake it — does not survive the wrap.
// It is invisible: nothing fails to compile, the assertion simply returns false
// and the caller concludes the tracker cannot do something it can. Every
// optional capability added to a provider needs a line here.
func (s *SigningProvider) AssignToReporter(ctx context.Context, key string) error {
	assigner, ok := s.Provider.(tracker.ReporterAssigner)
	if !ok {
		return tracker.ErrOwnershipUnsupported
	}
	return assigner.AssignToReporter(ctx, key)
}

// SigningCommenter is the Commenter-narrowed twin of SigningProvider, for the
// daemon's internal board posts which hold only a tracker.Commenter. It signs
// AddComment bodies with the same Sign helper and delegates ListComments.
type SigningCommenter struct {
	inner   tracker.Commenter
	machine string
	build   string
}

// NewSigningCommenter returns inner wrapped so AddComment signs marker bodies.
func NewSigningCommenter(inner tracker.Commenter, machine, build string) tracker.Commenter {
	return &SigningCommenter{inner: inner, machine: machine, build: build}
}

func (s *SigningCommenter) ListComments(ctx context.Context, key string) ([]tracker.Comment, error) {
	return s.inner.ListComments(ctx, key)
}

func (s *SigningCommenter) AddComment(ctx context.Context, key, body string) (*tracker.Comment, error) {
	return s.inner.AddComment(ctx, key, Sign(body, s.machine, s.build))
}
