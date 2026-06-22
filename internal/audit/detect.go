package audit

import (
	"strings"

	"github.com/gethuman-sh/human/internal/cliflags"
)

// MutatingOp is the skeleton of a detected mutating tracker command, decomposed
// into the parts the audit event needs.
type MutatingOp struct {
	Operation   string // "create","edit","delete","comment","status","start"
	TrackerKind string
	TrackerName string // from --tracker
	Key         string // issue key, empty for create
	Project     string // from --project, for create
}

// DetectMutating classifies a forwarded command's args into a mutating-op
// skeleton, returning ok=false for read-only or non-tracker commands.
//
// It is broader than the daemon's detectDestructive (which only gates
// confirmation-worthy destructive ops): audit must also cover non-destructive
// mutations like create and comment. It lives here rather than in the daemon so
// it is testable without a running daemon and reusable by a future client-side
// emitter. detectDestructive is deliberately left untouched.
func DetectMutating(args []string) (MutatingOp, bool) {
	trackerName, project := captureValueFlags(args)
	cleaned := stripFlags(args)

	// Locate the "issue" subcommand; everything before it is the tracker kind.
	trackerKind := ""
	issueIdx := -1
	for i, a := range cleaned {
		if a == "issue" || a == "issues" {
			issueIdx = i
			break
		}
		trackerKind = a
	}
	if issueIdx < 0 || issueIdx+1 >= len(cleaned) {
		return MutatingOp{}, false
	}

	op, ok := classifyVerb(cleaned, issueIdx)
	if !ok {
		return MutatingOp{}, false
	}
	op.TrackerKind = trackerKind
	op.TrackerName = trackerName
	op.Project = project
	return op, true
}

// captureValueFlags extracts --tracker and --project (both "--flag value" and
// "--flag=value" forms) before flag-stripping discards their values.
func captureValueFlags(args []string) (trackerName, project string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--tracker" && i+1 < len(args):
			trackerName = args[i+1]
		case strings.HasPrefix(a, "--tracker="):
			trackerName = strings.TrimPrefix(a, "--tracker=")
		case a == "--project" && i+1 < len(args):
			project = args[i+1]
		case strings.HasPrefix(a, "--project="):
			project = strings.TrimPrefix(a, "--project=")
		}
	}
	return trackerName, project
}

// stripFlags removes flags so only positional subcommands remain. A
// space-separated value flag (e.g. "--tracker work") must also drop its value
// token, otherwise that value shifts the positional indices. The known
// value-flag set is shared with client-side forwarding and detectDestructive
// via internal/cliflags so the three cannot drift apart.
func stripFlags(args []string) []string {
	cleaned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if cliflags.ValueFlags[a] && i+1 < len(args) {
				i++ // skip the flag's value token
			}
			continue
		}
		cleaned = append(cleaned, a)
	}
	return cleaned
}

// classifyVerb maps the verb (and its trailing positional key) at issueIdx+1
// into a MutatingOp, returning ok=false for read-only or malformed commands.
// Only the operation and key are filled; the caller adds tracker context.
func classifyVerb(cleaned []string, issueIdx int) (MutatingOp, bool) {
	verb := cleaned[issueIdx+1]

	// keyAt returns the positional token at the given offset past the verb, or
	// "" when absent.
	keyAt := func(offset int) string {
		idx := issueIdx + 1 + offset
		if idx < len(cleaned) {
			return cleaned[idx]
		}
		return ""
	}

	switch verb {
	case "create":
		return MutatingOp{Operation: "create"}, true
	case "edit", "delete", "status", "start":
		// Each requires a key as the next positional. "status" is exact, so the
		// read-only "statuses" listing verb never reaches here.
		if key := keyAt(1); key != "" {
			return MutatingOp{Operation: verb, Key: key}, true
		}
	case "comment":
		// Only "comment add <KEY>" mutates; "comment list <KEY>" is read-only.
		if keyAt(1) == "add" {
			if key := keyAt(2); key != "" {
				return MutatingOp{Operation: "comment", Key: key}, true
			}
		}
	}

	return MutatingOp{}, false
}
