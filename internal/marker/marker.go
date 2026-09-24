// Package marker implements the [human:*] comment protocol — the structured
// marker comments through which pipeline stages hand work to each other on a
// ticket (plan attached, ready for review, review verdict, deploy result).
// Agents previously assembled and re-parsed these blocks from prose templates
// in their prompts; this package is the single grammar both sides share.
package marker

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// Marker is one parsed [human:<type>] comment.
type Marker struct {
	// Type is the marker name inside the header, e.g. "ready-for-review".
	Type string `json:"type"`
	// Head is the optional token following the header on the same line
	// ([human:bug-verdict] confirmed → Head "confirmed").
	Head string `json:"head,omitempty"`
	// Fields are the "key: value" lines following the header, before the
	// first blank line. Indented continuation lines belong to the preceding
	// field (the reviews: map in review-complete).
	Fields map[string]string `json:"fields,omitempty"`
	// Body is the free-form remainder after the field block.
	Body string `json:"body,omitempty"`
}

// spec captures the validation contract of a known marker type. Unknown types
// stay postable — the protocol must stay open for new pipeline stages — but
// known types enforce their required fields and head enums so a malformed
// handoff fails at post time, not at the reader.
type spec struct {
	required []string
	// anyOf names fields of which AT LEAST ONE must be present — a contract
	// `required` cannot express. It is data rather than a closure so that
	// everything telling a caller how to post the marker can read it: a
	// hand-written validator satisfies Validate and leaves RequiredFields
	// answering "nothing required", which hands an agent a command that posts a
	// marker this package then rejects.
	anyOf []string
	// optional names fields a marker MAY carry that mean something to a
	// reader. Validate never consults it — an optional field cannot make a
	// marker invalid — but declaring it is what keeps a machine-read
	// distinction in the protocol instead of in prose, and it is what
	// `human fsm marker` reports to whoever has to post one.
	optional []string
	// fieldEnum closes the value set of a field: a value outside it is refused
	// at the post, the way a head token outside headEnum is. An absent field
	// is not an error — the field is still optional — but a present one must
	// mean one of the declared things, so a reader can group and act on it
	// instead of matching prose (SC-5250).
	fieldEnum map[string][]string
	headEnum  []string
	needsHead bool
	// validate carries a contract the field lists cannot express.
	validate func(Marker) error
}

// MinDecisionOptions is the fewest answers a decision block may offer. A block
// with one answer is not a decision: it parks the card until a human clicks the
// only thing on offer, which is a dead end dressed as a choice. Rejecting it at
// post time is what stops a stage from turning a condition it should have
// handled itself into a question.
const MinDecisionOptions = 2

// reservedOptionKeys are the option-block lines that are metadata rather than
// answers. `daemon` is the legacy provenance stamp; `machine` and `build` are the
// structured provenance fields every signed marker body now carries. All three
// share the `id: label` shape of a real answer — counting them is how a
// one-answer block passed for a two-answer one.
var reservedOptionKeys = map[string]bool{
	"stage": true, "context": true, "daemon": true,
	MachineField: true, BuildField: true,
}

// WaitsForPrefix marks the option-block line that says an answer DEFERS the
// work rather than directing it: `waits-for-<id>: <KEY>` means picking <id>
// holds this ticket until <KEY> is finished. It is the one thing about an
// answer the machine cannot read out of its prose — "SC-1 goes first" and "do
// it this way" are the same sentence to a parser — and without it every answer
// took the one path the machine knows, which is to start the work.
//
// Exported so the daemon's own block parser reserves the same prefix; two
// spellings of it would mean a line counted as an answer on one side and as
// metadata on the other.
const WaitsForPrefix = "waits-for-"

// isReservedOptionKey reports an option-block line that is metadata rather than
// an answer. Prefix membership as well as exact membership: the wait a
// sequencing answer declares is per-answer, so its key carries the answer's id.
func isReservedOptionKey(id string) bool {
	return reservedOptionKeys[id] || strings.HasPrefix(id, WaitsForPrefix)
}

// validateOptions counts the answers a decision block actually offers, and
// checks that every wait it declares belongs to one of them. They arrive as
// numbered fields when posted and as body lines when read back, so both are
// counted.
func validateOptions(m Marker) error {
	ids := map[string]bool{}
	waits := map[string]string{}
	for key, value := range m.Fields {
		collectOption(ids, waits, key, value)
	}
	for _, line := range strings.Split(m.Body, "\n") {
		id, label, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		collectOption(ids, waits, strings.TrimSpace(id), label)
	}
	if len(ids) < MinDecisionOptions {
		return errors.WithDetails(
			"a decision block must offer at least two answers",
			"type", m.Type, "answers", len(ids), "minimum", MinDecisionOptions)
	}
	// A wait naming no answer holds nothing: the block would render as an
	// ordinary fork and the ticket it was meant to wait for would be started over
	// anyway. Rejecting it at post time is the only moment anyone is watching.
	for id, key := range waits {
		if !ids[id] {
			return errors.WithDetails(
				"a decision block declares a wait for an answer it does not offer",
				"type", m.Type, "answer", id, "waits for", key)
		}
	}
	return nil
}

// collectOption sorts one `key: value` pair of a decision block into the
// answers it offers or the waits it declares. Shared by the posted-fields and
// read-back-body passes so the two cannot disagree about what an answer is.
func collectOption(ids map[string]bool, waits map[string]string, id, value string) {
	value = strings.TrimSpace(value)
	if id == "" || strings.ContainsAny(id, " \t") || value == "" {
		return
	}
	if answer, ok := strings.CutPrefix(id, WaitsForPrefix); ok {
		waits[answer] = value
		return
	}
	if !isReservedOptionKey(id) {
		ids[id] = true
	}
}

// EscalationField names WHICH determination a marker carries when one marker
// type is posted for more than one. [human:needs-planning] is posted both for
// an ordinary refusal and for the escalation raised once the plan re-drive
// bound is spent; the two used to be told apart by a sentence in the prose,
// which made the wording load-bearing and reclassified every escalation
// already on a ticket whenever it changed (SC-4245).
const (
	EscalationField     = "escalation"
	EscalationPlanStuck = "plan-stuck"
)

// BlockerFields are what a stop an agent chose (`needs-human-work`) carries on
// its *-failed marker: the blocker's kind, the evidence observed, what was
// attempted and the condition that releases the work (shared/exit-contract.md,
// SC-5179). Optional, because the daemon's own failure markers — a crash, a
// reap — have none of it; declared, so `human fsm marker` advertises them
// instead of leaving the contract in prose (`fsm where` prints only the
// required fields). A fresh slice per call, so no two specs share a backing
// array a caller could sort or append through.
func BlockerFields() []string { return []string{"kind", "evidence", "attempted", "release"} }

// BlockerKinds is the closed set a blocker's `kind` may name
// (shared/exit-contract.md): what a person or a later run can group stops
// by. A misspelling or an invented kind would land on the ticket looking
// classified while nothing downstream could act on it, so the set is
// enforced at the post (SC-5250).
func BlockerKinds() []string {
	return []string{"missing-permission", "unavailable-dependency", "exhausted-fix-rounds", "conflicting-requirements", "other"}
}

func blockerKindEnum() map[string][]string { return map[string][]string{"kind": BlockerKinds()} }

// SilenceReapFields are what the daemon's own silence reap records on the
// *-failed marker it posts: the observed silence, the idle budget it exceeded
// and what the agent had outstanding when it was judged (SC-5329). Declared
// beside the blocker fields so the give-up marker that quotes them, and
// `human fsm marker`, name the same contract.
func SilenceReapFields() []string { return []string{"idle", "budget", "outstanding"} }

// stageFailedOptional is the optional field set of a stage's *-failed marker:
// the headline, the blocker contract and the silence-reap record.
func stageFailedOptional() []string {
	return append(append([]string{"reason"}, BlockerFields()...), SilenceReapFields()...)
}

// NothingToDoReasons is the closed set of answers to WHY a ticket ended with
// nothing to plan. One terminal state stands for four determinations — the
// work is merged, another ticket carries it, a design decision has to come
// first, or it is not a real problem — and the board used to label all of
// them "already shipped", so a refusal read as delivery (SC-5326). The reason
// is required rather than optional because a record without one is exactly
// the lie this exists to stop.
func NothingToDoReasons() []string {
	return []string{"merged", "duplicate", "escalated", "rejected"}
}

func nothingToDoReasonEnum() map[string][]string {
	return map[string][]string{"reason": NothingToDoReasons()}
}

var specs = map[string]spec{
	"plan":                  {},
	"plan-ready":            {},
	"planning-failed":       {optional: stageFailedOptional(), fieldEnum: blockerKindEnum()},
	"implementation-failed": {optional: stageFailedOptional(), fieldEnum: blockerKindEnum()},
	// Two determinations share this header on purpose (SC-2990): the ordinary
	// refusal, and the plan-stuck escalation raised once PlanRedriveBound is
	// spent. Which one a comment is, is the escalation field — optional
	// because an ordinary refusal legitimately has none (SC-4245).
	"needs-planning": {optional: []string{EscalationField}},
	// `review` is optional and closed to one value on purpose: a handoff that
	// says nothing about who reviews it is the original contract — the daemon
	// chains a reviewer — and a handoff whose poster reviews the work itself
	// must say so in a word the daemon can check, not in prose it cannot
	// (SC-5476). A misspelt value is refused here rather than read as silence.
	"ready-for-review": {
		required:  []string{"branch", "commits"},
		optional:  []string{"review"},
		fieldEnum: map[string][]string{"review": {"inline"}},
	},
	"review-started":  {},
	"review-complete": {required: []string{"verdict"}},
	"review-failed":   {required: []string{"reason"}, optional: append(BlockerFields(), SilenceReapFields()...), fieldEnum: blockerKindEnum()},
	"no-fix-needed":   {required: []string{"verdict"}},
	"nothing-to-do":   {required: []string{"evidence", "reason"}, fieldEnum: nothingToDoReasonEnum()},
	"deploy-started":  {},
	"deploy-failed":   {required: []string{"reason"}, optional: append(BlockerFields(), SilenceReapFields()...), fieldEnum: blockerKindEnum()},
	// A deployed marker must say HOW the work shipped, and there are two honest
	// answers: through a pull request, or by a branch that was already in the
	// base when the deploy ran. Requiring pr outright made the second case
	// unpostable, so the daemon posted a bare header instead — a marker its own
	// protocol rejects, which is what routing every writer through this
	// validator surfaced. pr comes first because it is the ordinary shipping
	// path, and something that must name one field names that one.
	"deployed":    {anyOf: []string{"pr", "merged"}},
	"bug-verdict": {needsHead: true, headEnum: []string{"confirmed", "not-a-bug", "undetermined"}},
	"bug-verify":  {needsHead: true, headEnum: []string{"DONE", "NOT DONE"}},
	"options":     {required: []string{"stage"}, validate: validateOptions},
	// The ticket-review gate's verdict. The head carries the outcome so a reader
	// can classify it without parsing the body, exactly as bug-verdict does; the
	// gate ACTS on every outcome, so these name what it did, not what it asks for.
	"ticket-review": {
		needsHead: true,
		headEnum:  []string{"ready", "reframed", "superseded", "escalated", "rejected"},
	},
	"ticket-review-started": {},
	// Posted by the autofix skill at every terminal point and rendered by the
	// daemon (IssueDetailResult.FixSummaryHTML), so it is a first-class marker
	// rather than one of the open-ended types — declare it as such.
	"fix-summary": {},
	// The filing-time related-work triage (SC-2405). related-started brackets the
	// run's start; related carries the terminal verdict, its head naming which of
	// the three required statements it is — "found" (related work linked), "none"
	// (searched, nothing found), or "incomplete" (the run could not finish). The
	// enum is validated at post time so a malformed head never reaches the daemon's
	// hasCompletedRelatedRecord, which classifies found/none as a completed record.
	"related-started": {},
	"related":         {needsHead: true, headEnum: []string{"found", "none", "incomplete"}},
	// The background idea drafter (SC-4608). idea-draft-started brackets the
	// run's start; idea-draft is the PROVENANCE record, not the draft — the
	// draft is the ticket's description, and this says who last wrote it and
	// from what. author is required because the whole guard turns on it: a
	// record that cannot say whose words those are cannot protect them.
	"idea-draft-started": {},
	"idea-draft":         {required: []string{"author"}, optional: []string{"description", "source"}},
	// idea-draft-failed is posted by the DAEMON, not the drafter: a run that
	// dies on its first API call never gets a turn in which to report, which is
	// exactly the case that used to leave the ticket silent (SC-4820). Like the
	// other two it records content — an idea does not enter the pipeline — so it
	// moves nothing; `reason` is required for the same purpose as on every other
	// *-failed marker, so the reader is never handed a bare header.
	"idea-draft-failed": {required: []string{"reason"}},
	// The durable "shipped-partial" trace (SC-2910, the deferred deliverable of
	// SC-2848). Posted on the PM ticket when a deliberately-deferred acceptance
	// criterion is sanctioned (the planner's ship-narrow-plus-follow-on fork):
	// `follow-on` names the real ticket that now carries the deferred work,
	// `deferred` lists the criteria left out (one per line). It records a
	// decision, so both fields are required — a trace missing either is not a
	// trace. Not a stage transition (see ShippedPartialHeader): it decorates the
	// card, never moves it.
	"shipped-partial": {required: []string{"follow-on", "deferred"}},
}

// KnownTypes lists the marker types with a validation contract, sorted.
func KnownTypes() []string {
	types := make([]string, 0, len(specs))
	for t := range specs {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// RequiredFields lists the field lines a marker type must carry, sorted. Empty
// for a type with no contract and for an unknown one, which is the same answer
// Validate gives them.
//
// Exported so something telling a caller HOW to post a marker can read the
// contract instead of restating it. A second list of required fields would be
// wrong the first time a field is added here, and wrong silently: the caller
// would build a command that post-time validation then rejects.
func RequiredFields(markerType string) []string {
	s, known := specs[markerType]
	if !known || len(s.required) == 0 {
		return nil
	}
	out := append([]string(nil), s.required...)
	sort.Strings(out)
	return out
}

// AnyOfFields lists the fields a marker type must carry AT LEAST ONE of, in the
// order the contract prefers them — so a caller that has to name exactly one
// names the first. Empty when the type has no such contract.
//
// Exported for the same reason as RequiredFields: `human fsm where` builds the
// runnable `human marker post` command from these lists, and a contract it
// cannot read is a command that posts a marker Validate then rejects.
func AnyOfFields(markerType string) []string {
	s, known := specs[markerType]
	if !known || len(s.anyOf) == 0 {
		return nil
	}
	return append([]string(nil), s.anyOf...)
}

// OptionalFields lists the fields a marker type may carry that a reader
// understands, sorted. Empty for a type with no such fields and for an unknown
// one.
//
// Exported for the same reason as RequiredFields: a distinction the machine
// makes by reading a field is part of the contract, and a contract nothing can
// read is prose (SC-4245).
func OptionalFields(markerType string) []string {
	s, known := specs[markerType]
	if !known || len(s.optional) == 0 {
		return nil
	}
	out := append([]string(nil), s.optional...)
	sort.Strings(out)
	return out
}

// FieldValues reports the fields of a known type whose values are a closed
// set, with the set — what `human fsm marker` prints so an agent can check a
// value without reading prose. nil for a type that closes none.
func FieldValues(markerType string) map[string][]string {
	s, known := specs[markerType]
	if !known || len(s.fieldEnum) == 0 {
		return nil
	}
	out := make(map[string][]string, len(s.fieldEnum))
	for f, vals := range s.fieldEnum {
		out[f] = append([]string(nil), vals...)
	}
	return out
}

// validateFieldEnums refuses a present field whose value is outside its
// closed set. The set is in the message, not only the details: the agent
// that posted the marker sees the CLI's one line and must be able to correct
// the value from it.
func validateFieldEnums(s spec, m Marker) error {
	for field, allowed := range s.fieldEnum {
		if v := strings.TrimSpace(m.Fields[field]); v != "" && !slices.Contains(allowed, v) {
			return errors.WithDetails(fmt.Sprintf("marker field %s must be one of %s", field, strings.Join(allowed, "|")), "type", m.Type, "field", field, "value", v)
		}
	}
	return nil
}

// Validate checks m against its type's contract. Unknown types pass — only a
// syntactically valid type name is required.
func Validate(m Marker) error {
	if !typeNamePattern.MatchString(m.Type) {
		return errors.WithDetails("invalid marker type name", "type", m.Type)
	}
	s, known := specs[m.Type]
	if !known {
		return nil
	}
	for _, req := range s.required {
		if strings.TrimSpace(m.Fields[req]) == "" {
			return errors.WithDetails("marker is missing a required field", "type", m.Type, "field", req)
		}
	}
	if len(s.anyOf) > 0 && !slices.ContainsFunc(s.anyOf, func(f string) bool {
		return strings.TrimSpace(m.Fields[f]) != ""
	}) {
		return errors.WithDetails("marker must carry one of these fields", "type", m.Type,
			"one of", strings.Join(s.anyOf, "|"))
	}
	if s.needsHead && strings.TrimSpace(m.Head) == "" {
		return errors.WithDetails("marker requires a head token", "type", m.Type, "allowed", strings.Join(s.headEnum, "|"))
	}
	if len(s.headEnum) > 0 && m.Head != "" && !slices.Contains(s.headEnum, m.Head) {
		return errors.WithDetails("marker head token not in allowed set", "type", m.Type, "head", m.Head, "allowed", strings.Join(s.headEnum, "|"))
	}
	if err := validateFieldEnums(s, m); err != nil {
		return err
	}
	if s.validate != nil {
		return s.validate(m)
	}
	return nil
}

var (
	typeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	headerPattern   = regexp.MustCompile(`^\[human:([a-z][a-z0-9-]*)\][ \t]*(.*)$`)
	fieldPattern    = regexp.MustCompile(`^([a-z][a-z0-9_-]*):[ \t]*(.*)$`)
)

// Render serializes m into the canonical comment body: header line (with head
// token when present), field lines with indented continuations for multiline
// values, then a blank line and the body.
func Render(m Marker, fieldOrder []string) string {
	var b strings.Builder
	b.WriteString("[human:" + m.Type + "]")
	if m.Head != "" {
		b.WriteString(" " + m.Head)
	}
	b.WriteString("\n")
	for _, key := range orderedKeys(m.Fields, fieldOrder) {
		value := m.Fields[key]
		lines := strings.Split(value, "\n")
		b.WriteString(key + ": " + lines[0] + "\n")
		for _, cont := range lines[1:] {
			b.WriteString("  " + cont + "\n")
		}
	}
	if m.Body != "" {
		b.WriteString("\n" + strings.TrimSpace(m.Body) + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// orderedKeys returns fields' keys with the caller's explicit order first
// (posting order matters for readability: engineering, branch, commits) and
// any remaining keys sorted for stable output.
func orderedKeys(fields map[string]string, explicit []string) []string {
	seen := make(map[string]bool, len(fields))
	keys := make([]string, 0, len(fields))
	for _, key := range explicit {
		if _, ok := fields[key]; ok && !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	rest := make([]string, 0, len(fields))
	for key := range fields {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// ParseBody parses one comment body into a Marker. ok is false when the body
// is not a marker comment at all.
func ParseBody(body string) (Marker, bool) {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) == 0 {
		return Marker{}, false
	}
	header := headerPattern.FindStringSubmatch(strings.TrimSpace(lines[0]))
	if header == nil {
		return Marker{}, false
	}
	m := Marker{Type: header[1], Head: strings.TrimSpace(header[2])}

	fields, end, blankSeparator := scanFieldBlock(lines, 1)
	if len(fields) > 0 {
		m.Fields = fields
	}
	// A blank separator line is itself consumed (the body starts after it); a
	// non-field line that ended the block is already the first body line.
	bodyStart := end
	if blankSeparator {
		bodyStart++
	}
	if bodyStart < len(lines) {
		m.Body = strings.TrimSpace(strings.Join(lines[bodyStart:], "\n"))
	}
	return m, true
}

// scanFieldBlock scans lines[start:] for a marker's field block — the
// contiguous run of "key: value" lines and their indented continuations that
// follows the header — and returns the parsed fields plus where the block
// ends: end is the index of the line that ends it, and blankSeparator says
// whether that line is a true blank separator ("") rather than a non-field
// line the tolerant reader treats as the start of the body.
//
// Sign's insertFieldLines calls this too (for end alone) so the two
// functions' notion of "where do the fields end" — in particular the
// ordering between a continuation line and the blank-line boundary — cannot
// drift apart the way it did before: insertFieldLines tested the blank-line
// boundary first, so a signed marker whose field carried an embedded blank
// line (rendered as a "  " continuation) got its signature spliced inside
// that field instead of after it.
func scanFieldBlock(lines []string, start int) (fields map[string]string, end int, blankSeparator bool) {
	fields = map[string]string{}
	var currentField string
	for i := start; i < len(lines); i++ {
		line := lines[i]
		// A continuation line is checked before the blank-line boundary: Render
		// writes an empty continuation as "  " (the two-space indent with no
		// content), which trims to "" exactly like the true field/body separator
		// (a genuinely empty line). Only the separator has zero leading
		// whitespace, so testing the indent first keeps a verbatim value's
		// embedded blank line inside the field instead of truncating it and
		// spilling the rest of the fields into the body.
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && currentField != "" {
			fields[currentField] += "\n" + strings.TrimSpace(line)
			continue
		}
		if strings.TrimSpace(line) == "" {
			return fields, i, true
		}
		if match := fieldPattern.FindStringSubmatch(line); match != nil {
			currentField = match[1]
			fields[currentField] = match[2]
			continue
		}
		// A non-field, non-continuation line without a preceding blank line:
		// treat everything from here as body — tolerant reading beats
		// rejecting a slightly hand-edited marker.
		return fields, i, false
	}
	return fields, len(lines), false
}

// Latest returns the newest marker of markerType among comments, using the
// same latest-wins rule as the plan comment: a re-post supersedes older
// markers without history edits.
func Latest(comments []tracker.Comment, markerType string) (Marker, bool) {
	var latest Marker
	var found bool
	latestIdx := -1
	for i, c := range comments {
		m, ok := ParseBody(c.Body)
		if !ok || m.Type != markerType {
			continue
		}
		if latestIdx == -1 || c.Created.After(comments[latestIdx].Created) {
			latestIdx = i
			latest = m
			found = true
		}
	}
	return latest, found
}

// All returns every marker among comments, newest first.
func All(comments []tracker.Comment) []Marker {
	indexed := make([]int, 0, len(comments))
	for i := range comments {
		if _, ok := ParseBody(comments[i].Body); ok {
			indexed = append(indexed, i)
		}
	}
	sort.SliceStable(indexed, func(a, b int) bool {
		return comments[indexed[a]].Created.After(comments[indexed[b]].Created)
	})
	markers := make([]Marker, 0, len(indexed))
	for _, i := range indexed {
		m, _ := ParseBody(comments[i].Body)
		markers = append(markers, m)
	}
	return markers
}

// Prose returns a marker comment's free-form body — everything after the header
// and its field block — or "" when the body is not a marker at all.
//
// It is the supported way to read a marker's content from outside this package.
// Callers used to strip the header with TrimPrefix and take what followed, which
// was correct only until Sign began splicing machine:/build: in between; after
// that the "content" started with the signature. Routing every reader through
// one function is what keeps a future field addition from repeating that.
func Prose(body string) string {
	m, ok := ParseBody(body)
	if !ok {
		return ""
	}
	return strings.TrimSpace(m.Body)
}

// Head returns a marker's head token — the word following the header on the same
// line, e.g. "found" in "[human:related] found" — or "" when the body is not a
// marker.
func Head(body string) string {
	m, ok := ParseBody(body)
	if !ok {
		return ""
	}
	return strings.TrimSpace(m.Head)
}
