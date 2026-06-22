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
	// Capture --tracker and --project before stripping value-flag tokens, since
	// stripping would otherwise discard their values along with the flags.
	var trackerName, project string
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

	// Strip flags to leave only positional subcommands. A space-separated value
	// flag (e.g. "--tracker work") must also drop its value token, otherwise
	// that value shifts the positional indices. The known value-flag set is
	// shared with client-side forwarding and detectDestructive via
	// internal/cliflags so the three cannot drift apart.
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

	verb := cleaned[issueIdx+1]
	op := MutatingOp{TrackerKind: trackerKind, TrackerName: trackerName, Project: project}

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
		op.Operation = "create"
		return op, true
	case "edit":
		if key := keyAt(1); key != "" {
			op.Operation = "edit"
			op.Key = key
			return op, true
		}
	case "delete":
		if key := keyAt(1); key != "" {
			op.Operation = "delete"
			op.Key = key
			return op, true
		}
	case "comment":
		// Only "comment add <KEY>" mutates; "comment list <KEY>" is read-only.
		if keyAt(1) == "add" {
			if key := keyAt(2); key != "" {
				op.Operation = "comment"
				op.Key = key
				return op, true
			}
		}
	case "status":
		// Exact "status" mutates state; the "statuses" listing verb does not.
		if key := keyAt(1); key != "" {
			op.Operation = "status"
			op.Key = key
			return op, true
		}
	case "start":
		if key := keyAt(1); key != "" {
			op.Operation = "start"
			op.Key = key
			return op, true
		}
	}

	return MutatingOp{}, false
}
