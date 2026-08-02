// Package marker implements the [human:*] comment protocol — the structured
// marker comments through which pipeline stages hand work to each other on a
// ticket (plan attached, ready for review, review verdict, deploy result).
// Agents previously assembled and re-parsed these blocks from prose templates
// in their prompts; this package is the single grammar both sides share.
package marker

import (
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
	required  []string
	headEnum  []string
	needsHead bool
	// validate carries a contract the required/head fields cannot express.
	validate func(Marker) error
}

// MinDecisionOptions is the fewest answers a decision block may offer. A block
// with one answer is not a decision: it parks the card until a human clicks the
// only thing on offer, which is a dead end dressed as a choice. Rejecting it at
// post time is what stops a stage from turning a condition it should have
// handled itself into a question.
const MinDecisionOptions = 2

// reservedOptionKeys are the option-block lines that are metadata rather than
// answers. `daemon` is the provenance stamp every marker body carries, and it
// has the same `id: label` shape as a real answer — counting it is how a
// one-answer block passed for a two-answer one.
var reservedOptionKeys = map[string]bool{"stage": true, "context": true, "daemon": true}

// validateOptions counts the answers a decision block actually offers. They
// arrive as numbered fields when posted and as body lines when read back, so
// both are counted.
func validateOptions(m Marker) error {
	ids := map[string]bool{}
	for key, value := range m.Fields {
		if !reservedOptionKeys[key] && strings.TrimSpace(value) != "" {
			ids[key] = true
		}
	}
	for _, line := range strings.Split(m.Body, "\n") {
		line = strings.TrimSpace(line)
		id, label, ok := strings.Cut(line, ":")
		id = strings.TrimSpace(id)
		if !ok || id == "" || strings.ContainsAny(id, " \t") || reservedOptionKeys[id] {
			continue
		}
		if strings.TrimSpace(label) != "" {
			ids[id] = true
		}
	}
	if len(ids) < MinDecisionOptions {
		return errors.WithDetails(
			"a decision block must offer at least two answers",
			"type", m.Type, "answers", len(ids), "minimum", MinDecisionOptions)
	}
	return nil
}

var specs = map[string]spec{
	"plan":                  {},
	"plan-ready":            {},
	"planning-failed":       {},
	"implementation-failed": {},
	"needs-planning":        {},
	"ready-for-review":      {required: []string{"branch", "commits"}},
	"review-started":        {},
	"review-complete":       {required: []string{"verdict"}},
	"review-failed":         {required: []string{"reason"}},
	"no-fix-needed":         {required: []string{"verdict"}},
	"nothing-to-do":         {required: []string{"evidence"}},
	"deploy-started":        {},
	"deploy-failed":         {required: []string{"reason"}},
	"deployed":              {required: []string{"pr"}},
	"bug-verdict":           {needsHead: true, headEnum: []string{"confirmed", "not-a-bug", "undetermined"}},
	"bug-verify":            {needsHead: true, headEnum: []string{"DONE", "NOT DONE"}},
	"options":               {required: []string{"stage"}, validate: validateOptions},
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
	if s.needsHead && strings.TrimSpace(m.Head) == "" {
		return errors.WithDetails("marker requires a head token", "type", m.Type, "allowed", strings.Join(s.headEnum, "|"))
	}
	if len(s.headEnum) > 0 && m.Head != "" && !slices.Contains(s.headEnum, m.Head) {
		return errors.WithDetails("marker head token not in allowed set", "type", m.Type, "head", m.Head, "allowed", strings.Join(s.headEnum, "|"))
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

	fields := map[string]string{}
	var currentField string
	bodyStart := len(lines)
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			bodyStart = i + 1
			break
		}
		if match := fieldPattern.FindStringSubmatch(line); match != nil {
			currentField = match[1]
			fields[currentField] = match[2]
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && currentField != "" {
			fields[currentField] += "\n" + strings.TrimSpace(line)
			continue
		}
		// A non-field, non-continuation line without a preceding blank line:
		// treat everything from here as body — tolerant reading beats
		// rejecting a slightly hand-edited marker.
		bodyStart = i
		break
	}
	if len(fields) > 0 {
		m.Fields = fields
	}
	if bodyStart < len(lines) {
		m.Body = strings.TrimSpace(strings.Join(lines[bodyStart:], "\n"))
	}
	return m, true
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
