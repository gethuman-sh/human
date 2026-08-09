package daemon

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/proxy"
)

// endingKind names how a stopped stage should be treated when the stop was the
// machine being unavailable rather than the work being wrong.
type endingKind int

const (
	endingUnknown     endingKind = iota // no unavailability signal — existing logic decides
	endingPaused                        // transient substrate loss: uncharged outage, resumes itself
	endingNeedsPerson                   // a wall that does not self-heal: uncharged red, names the action
)

// classifyUnavailability folds the hook errorType and the model-boundary
// outcome class into one verdict, so a refusal that kills the agent before it
// records an exit is still recognised (the SC-2856 incident: a session-limit
// refusal never reaches the hook's ordinary exit-recording path). reason is
// the substrate phrase for the card face; empty when kind is endingUnknown.
//
// The hook's own errorType is checked first — it is the most direct signal,
// present even when the agent died before any model-boundary call could be
// attributed. The model-call outcome class is the fallback: it is read from
// the live network boundary, so it recognises the same unavailability even
// when the refusal carried no hook errorType at all.
func classifyUnavailability(errorType string, latest LatestOutcomeClass, pmKey, stage string) (kind endingKind, reason string) {
	if kind, reason = classifyErrorType(errorType); kind != endingUnknown {
		return kind, reason
	}
	if latest == nil {
		return endingUnknown, ""
	}
	class, ok := latest(pmKey, stage)
	if !ok {
		return endingUnknown, ""
	}
	return classifyOutcomeClass(class)
}

// classifyErrorType maps the hook event's own error type. "" (no signal) and
// any errorType this table does not recognise both return endingUnknown, so
// classifyUnavailability falls through to the model-boundary class.
func classifyErrorType(errorType string) (endingKind, string) {
	t := strings.ToLower(strings.TrimSpace(errorType))
	switch {
	case t == "":
		return endingUnknown, ""
	case t == "rate_limit" || t == "rate-limit":
		return endingPaused, "model usage limit"
	case t == "overloaded" || t == "overloaded_error":
		return endingPaused, "model API overloaded"
	// The model API's own 5xx. It is the substrate failing, not the work, and
	// Claude Code retries through it — so a run that carries on past one must not
	// have been charged for it, and one that does not is still an outage rather
	// than a stage that decided something wrong (SC-4026).
	case t == "server_error" || t == "api_error":
		return endingPaused, "model API returned an error"
	case t == "authentication_error" || t == "auth":
		return endingNeedsPerson, "model API authentication was refused"
	case strings.Contains(t, "billing") || strings.Contains(t, "credit"):
		return endingNeedsPerson, "model API billing/credit limit reached"
	default:
		return endingUnknown, ""
	}
}

// classifyOutcomeClass maps a proxy model-call outcome class to an ending
// kind, mirroring classifyErrorType for the signals only the network boundary
// carries (a healthy call, a plain "other" status, or one this table does not
// recognise, are all endingUnknown — the existing logic decides).
func classifyOutcomeClass(class string) (endingKind, string) {
	switch class {
	case proxy.ClassRateLimit:
		return endingPaused, "model usage limit"
	case proxy.ClassOverload:
		return endingPaused, "model API overloaded"
	case proxy.ClassNetwork:
		return endingPaused, "could not reach the model API"
	case proxy.ClassAuth:
		return endingNeedsPerson, "model API authentication was refused"
	case proxy.ClassSpendLimit:
		return endingNeedsPerson, "model API billing/credit limit reached"
	default:
		return endingUnknown, ""
	}
}

// resumeTimeRe matches a stated recovery time such as "resets 8:50am (UTC)" or
// "resets 22:15" — case-insensitive, with an optional am/pm and an optional
// named timezone in parentheses.
var resumeTimeRe = regexp.MustCompile(`(?i)resets?\s+(\d{1,2}):(\d{2})\s*(am|pm)?\s*(?:\(([A-Za-z_/+-]+)\))?`)

// parseResumeTime scans a diagnosis (headline + log-tail detail) for a stated
// recovery time such as "resets 8:50am (UTC)" / "resets 10:50". It returns the
// next absolute instant matching that wall-clock, in loc (or the named zone
// when the text states one), strictly after now. (time.Time{}, false) when no
// such time is stated or the match cannot be resolved to a real time.
func parseResumeTime(text string, now time.Time, loc *time.Location) (time.Time, bool) {
	m := resumeTimeRe.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	hour, err := strconv.Atoi(m[1])
	if err != nil || hour > 23 {
		return time.Time{}, false
	}
	minute, err := strconv.Atoi(m[2])
	if err != nil || minute > 59 {
		return time.Time{}, false
	}
	switch strings.ToLower(m[3]) {
	case "am":
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour != 12 {
			hour += 12
		}
	}
	if hour > 23 {
		return time.Time{}, false
	}
	zone := loc
	if named := strings.TrimSpace(m[4]); named != "" {
		if z, err := time.LoadLocation(named); err == nil {
			zone = z
		}
	}
	nowInZone := now.In(zone)
	candidate := time.Date(nowInZone.Year(), nowInZone.Month(), nowInZone.Day(), hour, minute, 0, 0, zone)
	if !candidate.After(nowInZone) {
		// A stated time that is not still ahead of now means tomorrow — the
		// refusal names the next occurrence of that wall-clock instant.
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate.UTC(), true
}
