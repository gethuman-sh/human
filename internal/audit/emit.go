package audit

import (
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/cliflags"
)

// DecisionFromEnv reads the at-decision-time context an agent harness forwards
// via HUMAN_AUDIT_* environment variables. All fields are optional: a missing
// rationale is allowed and recorded empty (the event still captures
// actor/resource/operation/outcome/model).
func DecisionFromEnv(lookup func(string) string) DecisionContext {
	return DecisionContext{
		ModelID:      lookup("HUMAN_AUDIT_MODEL_ID"),
		ModelVersion: lookup("HUMAN_AUDIT_MODEL_VERSION"),
		Inputs:       lookup("HUMAN_AUDIT_INPUTS"),
		Rationale:    lookup("HUMAN_AUDIT_RATIONALE"),
	}
}

// BuildEvent assembles a complete CloudEvents envelope from a detected op, the
// observed outcome, the at-decision-time context, and the original args. The
// args are stripped of credentials first so secrets never reach the durable,
// reviewable trail.
func BuildEvent(now time.Time, op MutatingOp, outcome Outcome, dc DecisionContext, args []string) (Event, error) {
	data := Data{
		Operation: op.Operation,
		Actor:     Actor{TrackerKind: op.TrackerKind, TrackerName: op.TrackerName},
		Resource:  Resource{Key: op.Key, Project: op.Project},
		Outcome:   outcome,
		Args:      stripCredentials(args),
		Decision:  dc,
	}

	id, err := newID()
	if err != nil {
		return Event{}, err
	}
	return buildEventWithID(id, now, data), nil
}

// stripCredentials removes secret-bearing tokens from args so the audit trail
// stays safe to review and share. It drops any value-flag token and the value
// that follows it, plus the inline "--*-token=" / "--*-key=" forms. The
// shared cliflags.ValueFlags set keeps this aligned with the detector and
// client-side forwarding.
func stripCredentials(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if cliflags.ValueFlags[a] {
			i++ // also skip the value token that carries the secret
			continue
		}
		if isInlineCredential(a) {
			continue
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isInlineCredential reports whether a is an inline "--<x>-token=…" or
// "--<x>-key=…" flag carrying a secret value in the same token.
func isInlineCredential(a string) bool {
	eq := strings.IndexByte(a, '=')
	if eq < 0 {
		return false
	}
	name := a[:eq]
	return strings.HasSuffix(name, "-token") || strings.HasSuffix(name, "-key")
}
