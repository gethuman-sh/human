// Package ideadraft owns the rule that decides whether the machine may write
// an idea ticket's description, and the provenance record that rule reads.
//
// It is a package of its own because the decision must be identical on every
// machine and in every process that could make it — the CLI command the
// drafter runs, and the desktop's description editor recording a human edit.
// The rule reads only the ticket and its comments, never local state, so a
// second machine with no memory of the ticket reaches the same verdict.
package ideadraft

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

const (
	// MarkerType is the provenance record's marker type.
	MarkerType = "idea-draft"
	// StartedMarkerType brackets a drafting run's start, like related-started.
	StartedMarkerType = "idea-draft-started"

	// FieldAuthor says whose words the description currently holds. It is the
	// field the whole guard turns on.
	FieldAuthor = "author"
	// FieldDescription fingerprints the text that was written, so a later run
	// can tell its own words from an edit made since.
	FieldDescription = "description"
	// FieldSource fingerprints the INPUT a draft was made from. It is the loop
	// breaker: the drafter's own write bumps the ticket's UpdatedAt, and
	// without this a run would keep redrafting text it wrote itself.
	FieldSource = "source"

	// AuthorMachine and AuthorHuman are the only two authors a record names.
	AuthorMachine = "machine"
	AuthorHuman   = "human"

	// TBAToken is the literal a drafter leaves where it would otherwise have
	// assumed. Counted, never parsed: the question after it is prose.
	TBAToken = "[TBA:"
)

// FieldOrder is the order a provenance record's fields are rendered in, so
// every writer produces the same bytes for the same record.
var FieldOrder = []string{FieldAuthor, FieldDescription, FieldSource}

// Verdict is what the guard permits.
type Verdict string

// Reason names WHY a verdict was reached. It is a value rather than prose so a
// caller can branch on it — the stand-downs are not interchangeable, and the
// one that means "this ticket is no longer an idea" must not be recorded as
// "a human wrote these words".
type Reason string

const (
	// ReasonNotAnIdea is a stand-down about the ticket's LABELS, not about who
	// wrote its description.
	ReasonNotAnIdea Reason = "not an idea ticket"
	// ReasonHumanAuthored is a stand-down on an existing human record.
	ReasonHumanAuthored Reason = "a human authored this description"
	// ReasonNoPriorDraft admits the first draft.
	ReasonNoPriorDraft Reason = "no description and no prior draft"
	// ReasonUnknownProvenance is a stand-down on words nobody recorded writing.
	ReasonUnknownProvenance Reason = "description of unknown provenance"
	// ReasonChangedSinceDraft is a stand-down on words edited since the machine
	// wrote them.
	ReasonChangedSinceDraft Reason = "description changed since the machine wrote it"
	// ReasonAlreadyCurrent means the draft still matches its input.
	ReasonAlreadyCurrent Reason = "the draft is already current"
	// ReasonSourceChanged admits a redraft: the machine owns the description
	// and the input it was drafted from has changed.
	ReasonSourceChanged Reason = "the machine wrote the current description and its source changed"
)

const (
	// VerdictWrite means the machine may write its draft.
	VerdictWrite Verdict = "write"
	// VerdictStandDown means the description is human-authored: never write.
	VerdictStandDown Verdict = "stand-down"
	// VerdictCurrent means the draft is already current; nothing to do.
	VerdictCurrent Verdict = "current"
)

// Fingerprint is the provenance value: "sha256:<hex>" of the exact bytes.
// Exact — no trimming, no normalisation — because "byte-identical to what the
// machine wrote" is the whole guarantee.
func Fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SourceFingerprint is the fingerprint of the input a draft is made from.
// The title is the whole input: a description the machine may draft over is
// either empty or its own previous draft, so folding it in would make every
// draft change its own source and redraft forever.
func SourceFingerprint(title string) string { return Fingerprint(title) }

// TBACount counts the unanswered gaps in a description.
func TBACount(desc string) int { return strings.Count(desc, TBAToken) }

// Provenance is the parsed [human:idea-draft] record.
type Provenance struct {
	Author      string
	Description string // fingerprint of what was written
	Source      string // fingerprint of the input it was drafted from
	Found       bool
}

// LatestProvenance reads the newest [human:idea-draft] record from comments.
// An absent author reads as the machine: only this package writes the record
// and it always writes the field, so the default keeps a hand-posted record
// from silently claiming a human's protection.
func LatestProvenance(comments []tracker.Comment) Provenance {
	m, ok := marker.Latest(comments, MarkerType)
	if !ok {
		return Provenance{}
	}
	p := Provenance{
		Author:      m.Fields[FieldAuthor],
		Description: m.Fields[FieldDescription],
		Source:      m.Fields[FieldSource],
		Found:       true,
	}
	if p.Author == "" {
		p.Author = AuthorMachine
	}
	return p
}

// Decide answers whether the machine may write this ticket's description, and
// why. It reads nothing but its arguments so that a second machine, with no
// memory of the ticket, reaches the same verdict from the tracker alone.
//
// It is deliberately fail-safe: every condition it cannot account for lands on
// stand-down, because writing over a human's words costs more than skipping a
// draft.
func Decide(isIdea bool, title, description string, comments []tracker.Comment) (Verdict, Reason) {
	// Promotion removes the idea labels while a debounced redraft may still be
	// armed for the key; a ticket that is no longer an idea is being edited by
	// a person and must never be written into.
	if !isIdea {
		return VerdictStandDown, ReasonNotAnIdea
	}
	p := LatestProvenance(comments)
	if p.Found && p.Author == AuthorHuman {
		return VerdictStandDown, ReasonHumanAuthored
	}
	if !p.Found {
		if strings.TrimSpace(description) == "" {
			return VerdictWrite, ReasonNoPriorDraft
		}
		return VerdictStandDown, ReasonUnknownProvenance
	}
	if description != "" && Fingerprint(description) != p.Description {
		return VerdictStandDown, ReasonChangedSinceDraft
	}
	if SourceFingerprint(title) == p.Source {
		return VerdictCurrent, ReasonAlreadyCurrent
	}
	return VerdictWrite, ReasonSourceChanged
}

// PinsHuman reports whether a stand-down should be recorded as human
// authorship. Only the ones ABOUT the description qualify: pinning a
// stand-down that merely means "this ticket carries no idea label any more"
// would freeze the description of a ticket that is later re-labelled as an
// idea, and nothing would ever draft it again.
func PinsHuman(v Verdict, r Reason) bool {
	return v == VerdictStandDown && r != ReasonNotAnIdea
}

// MachineRecord builds the provenance marker for text the machine just wrote.
// It carries fingerprints only — the draft itself is the ticket's description.
func MachineRecord(written, title string) marker.Marker {
	return marker.Marker{
		Type: MarkerType,
		Fields: map[string]string{
			FieldAuthor:      AuthorMachine,
			FieldDescription: Fingerprint(written),
			FieldSource:      SourceFingerprint(title),
		},
	}
}

// HumanRecord builds the provenance marker that pins a description as
// human-authored — posted when a run stands down, and when the description
// editor applies a human's edit. It records no source: there is no input the
// machine could redraft from that would make these words its own again.
func HumanRecord(current string) marker.Marker {
	return marker.Marker{
		Type: MarkerType,
		Fields: map[string]string{
			FieldAuthor:      AuthorHuman,
			FieldDescription: Fingerprint(current),
		},
	}
}
