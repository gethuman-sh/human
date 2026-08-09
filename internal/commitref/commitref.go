// Package commitref owns the commit-message issue-reference grammar: which
// forms are accepted, how a message is checked for one, how a reference to a
// given key is searched for, and how the keys in a subject are read back out.
//
// It exists because that grammar was written four times — an extended-regexp in
// .githooks/commit-msg, a grep-pattern builder in internal/gitrepo whose own
// comment admitted it mirrored the hook, an extraction pair beside it, and the
// canonical-form rule in internal/tracker. Four spellings of one rule is four
// places to change and three chances to forget, and the failure is quiet in
// both directions: a form the hook accepts but the search does not is a commit
// nobody can find afterwards, and a form the search accepts but the hook does
// not is a commit nobody could have made.
//
// The hook stays shell — a hook that needs the binary built cannot be the thing
// that gates building it — so it is not a fifth caller but a second spelling
// held in step by a shared corpus both sides are run over (corpus.go).
package commitref

import (
	"regexp"
	"strings"
)

// Form is one accepted way to reference an issue in a commit message. The list
// is the grammar: something that means to explain the rule to a person reads
// these rather than restating them.
type Form struct {
	// Name is what a person calls this form.
	Name string
	// Example is a message fragment in this form, shown when a commit is
	// rejected and used as a case in the corpus.
	Example string
	pattern *regexp.Regexp
}

// forms are the accepted reference styles, in the order the rejection message
// lists them. Each mirrors one alternative of the hook's own pattern.
var forms = []Form{
	{
		Name:    "numeric",
		Example: "Issue #123",
		pattern: regexp.MustCompile(`Issue\s+#[0-9]+`),
	},
	{
		Name:    "prefixed",
		Example: "Issue HUM-30",
		pattern: regexp.MustCompile(`Issue\s+[A-Z]+-[0-9]+`),
	},
	{
		Name:    "bracket",
		Example: "[SC-57]",
		pattern: regexp.MustCompile(`\[[A-Z]+-[0-9]+`),
	},
	{
		Name:    "code host",
		Example: "octocat/repo#42",
		pattern: regexp.MustCompile(`[A-Za-z0-9._-]+/[A-Za-z0-9._-]+#[0-9]+`),
	},
	{
		Name:    "project path",
		Example: "MyProject/42",
		pattern: regexp.MustCompile(`[A-Za-z][A-Za-z0-9._-]*/[0-9]+`),
	},
}

// Forms lists the accepted reference styles.
func Forms() []Form {
	out := make([]Form, len(forms))
	copy(out, forms)
	return out
}

// HasAny reports whether message carries an issue reference in any accepted
// form — the question the commit-msg hook asks.
func HasAny(message string) bool {
	for _, f := range forms {
		if f.pattern.MatchString(message) {
			return true
		}
	}
	return false
}

// exemptSubject matches the commit kinds that need no reference: a merge, a
// revert, and the two rebase-fixup prefixes, all of which take their subject
// from a commit that already carried one.
var exemptSubject = regexp.MustCompile(`^(Merge |Revert "|fixup! |squash! )`)

// Exempt reports whether a message's own kind excuses it from carrying a
// reference. It reads the FIRST line only: a body quoting a merge is still an
// ordinary commit.
func Exempt(message string) bool {
	first, _, _ := strings.Cut(message, "\n")
	return exemptSubject.MatchString(first)
}

var (
	// prefixedKey and numericRef are the extraction grammar for reading keys
	// back OUT of a subject: bracketed-or-bare PREFIX-N keys, and #N numeric
	// references.
	prefixedKey = regexp.MustCompile(`\[?([A-Z]{2,}-[0-9]+)\]?`)
	numericRef  = regexp.MustCompile(`#([0-9]+)`)
	numericKey  = regexp.MustCompile(`^[0-9]+$`)
)

// Keys extracts the ticket keys a subject references: every prefixed key
// first, then every numeric one, each deduped in order of first appearance.
//
// The two groups are kept apart rather than interleaved because a caller
// listing "the tickets this touched" wants the named ones first — a numeric
// reference cannot be told from an issue number in another system by looking.
func Keys(subjects []string) []string {
	var prefixed, numeric []string
	seen := map[string]bool{}
	for _, subject := range subjects {
		for _, m := range prefixedKey.FindAllStringSubmatch(subject, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				prefixed = append(prefixed, m[1])
			}
		}
		for _, m := range numericRef.FindAllStringSubmatch(subject, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				numeric = append(numeric, m[1])
			}
		}
	}
	return append(prefixed, numeric...)
}

// IsNumericKey reports whether a key is purely numeric — the one key shape that
// carries no prefix of its own and so has to be told what tracker it belongs to
// before it can be written into a commit message.
func IsNumericKey(key string) bool { return numericKey.MatchString(key) }

// TrimBrackets strips surrounding whitespace and one layer of [ ] from a key,
// the form the board and pipeline sometimes pass internally.
func TrimBrackets(key string) string {
	trimmed := strings.TrimSpace(key)
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
}

// GrepPattern builds the extended-regexp `git log --grep` pattern matching
// every accepted reference form for one key.
//
// The guards are the whole difficulty: a numeric key must not match inside a
// longer number, and a prefixed key must not match inside a longer key — SC-5
// finding every SC-57 commit is a wrong answer that looks like a right one.
func GrepPattern(key string) string {
	esc := regexp.QuoteMeta(key)
	if IsNumericKey(key) {
		return `\[#?` + esc + `\]|(^|[^0-9])#` + esc + `([^0-9]|$)|Issue #?` + esc + `([^0-9]|$)|/` + esc + `([^0-9]|$)`
	}
	return `\[` + esc + `\]|Issue ` + esc + `([^0-9]|$)|(^|[^A-Za-z0-9-])` + esc + `([^0-9]|$)`
}
