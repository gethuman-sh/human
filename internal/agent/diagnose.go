package agent

// Post-mortem failure diagnosis for board agent runs. When a stage agent dies,
// the daemon's failure watcher used to post a bare "agent exited without
// completing the stage" — every real cause then had to be dug out of the run's
// artifacts by hand. All the material is already persisted per run by the
// execution log store (output.log, outcome.json, launch.json); this file reads
// it back and distills a headline + detail block for the failed marker.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// FailureDiagnosis is the distilled cause of a dead agent run. Headline is a
// single plain-text line (the board card's badge/tooltip text and the first
// body line of the failed marker); Detail is a markdown block for the ticket's
// detail pane.
type FailureDiagnosis struct {
	Headline string
	Detail   string
}

const (
	// diagnoseTailLines lines of output are quoted in the detail block; the
	// wider diagnoseScanLines window is searched for error lines and the exit
	// trailer.
	diagnoseTailLines   = 15
	diagnoseScanLines   = 200
	diagnoseMaxHeadline = 200
	diagnoseMaxDetail   = 2000
	diagnoseMaxLineLen  = 300
)

// The wait exists because artifact availability depends on how the run died:
// on the live hook path the tee's exit trailer may still be in flight when the
// Stop event arrives, while a reaped run already has outcome.json. Package
// vars so tests can zero the wait.
var (
	diagnoseWaitStep  = 2 * time.Second
	diagnoseWaitTries = 3
)

// genericFailureHeadline is the pre-diagnosis wording, kept as the terminal
// fallback so a run with no readable artifacts reports exactly what it always
// did.
const genericFailureHeadline = "agent exited without completing the stage"

// DiagnoseFailure inspects the agent's latest execution artifacts (outcome.json,
// output.log tail) plus the hook event's error type and distills why the run
// died. It never fails: missing or unreadable artifacts degrade stepwise down
// to the generic headline. hookErrorType is the hook event's ErrorType ("" when
// the event carried none).
func DiagnoseFailure(agentName, hookErrorType string) FailureDiagnosis {
	exe, err := LatestExecution(agentName)
	if err != nil {
		return FailureDiagnosis{Headline: headlineFor(hookErrorType, false, 0, false, "")}
	}
	outcome := waitForRunEnd(exe.Dir())
	scan := readOutputTail(exe.Dir(), diagnoseScanLines)
	exitCode, haveExit := parseExitTrailer(scan)
	errLine := sanitizeForTracker(lastErrorLine(scan))

	reaped := outcome != nil && outcome.Reason == "reaped"
	headline := truncateRunes(headlineFor(hookErrorType, reaped, exitCode, haveExit, errLine), diagnoseMaxHeadline)
	detail := truncateRunes(detailFor(exe, outcome, exitCode, haveExit, scan), diagnoseMaxDetail)
	return FailureDiagnosis{Headline: headline, Detail: detail}
}

// waitForRunEnd waits briefly until the run's end-of-life evidence exists —
// outcome.json (reap path) or the tee's exit trailer (live path) — then
// returns whatever outcome is readable. nil is a valid answer: the trailer
// alone carries the exit code.
func waitForRunEnd(dir string) *OutcomeRecord {
	for try := 0; ; try++ {
		var oc OutcomeRecord
		if err := readJSONFile(filepath.Join(dir, "outcome.json"), &oc); err == nil {
			return &oc
		}
		if _, ok := parseExitTrailer(readOutputTail(dir, 5)); ok {
			return nil
		}
		if try >= diagnoseWaitTries-1 {
			return nil
		}
		time.Sleep(diagnoseWaitStep)
	}
}

// readOutputTail returns the last n non-trailing-blank lines of the run's
// output.log, or no lines when the log is missing/unreadable.
func readOutputTail(dir string, n int) []string {
	data, err := os.ReadFile(filepath.Join(dir, "output.log")) // #nosec G304 -- path derived from validated agent name + hex id
	if err != nil {
		return []string{}
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// ansiRe strips terminal escape sequences before pattern matching — claude's
// output is colored, and a colored "Error:" must still match.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// parseExitTrailer finds the tee's exit trailer in the scanned lines, newest
// first, and returns the recorded exit code.
func parseExitTrailer(lines []string) (int, bool) {
	for _, line := range slices.Backward(lines) {
		line := strings.TrimSpace(stripANSI(line))
		if !strings.HasPrefix(line, execExitTrailerPrefix) {
			continue
		}
		code, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, execExitTrailerPrefix)))
		if err != nil {
			continue
		}
		return code, true
	}
	return 0, false
}

// errorLineRes are the shapes a "real" error line takes in claude/tool output.
// Heuristic by design: a false positive still shows a line from the actual run,
// which beats the generic message.
var errorLineRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(error|fatal|panic)[:\s]`),
	regexp.MustCompile(`(?i)api.?error|overloaded_error|rate.?limit`),
	regexp.MustCompile(`(?i)invalid_request_error|authentication_error|permission_error|billing`),
	regexp.MustCompile(`(?i)context deadline exceeded|connection refused|no space left on device|out of memory|\bkilled\b`),
}

// lastErrorLine returns the last line of the scan window that looks like an
// error, skipping the exit trailer itself.
func lastErrorLine(lines []string) string {
	for _, line := range slices.Backward(lines) {
		line := strings.TrimSpace(stripANSI(line))
		if line == "" || strings.HasPrefix(line, execExitTrailerPrefix) {
			continue
		}
		for _, re := range errorLineRes {
			if re.MatchString(line) {
				return line
			}
		}
	}
	return ""
}

// headlineFor implements the interpretation table, most-specific signal first:
// the hook's own error type beats artifact inference, a reap beats exit codes
// (the reaped process's code is the killer's, not the cause), known exit codes
// beat the error-line heuristic.
func headlineFor(hookErrorType string, reaped bool, exitCode int, haveExit bool, errLine string) string {
	if h := hookHeadline(hookErrorType); h != "" {
		return h
	}
	if reaped {
		return "agent process died and was reaped (crashed or killed; daemon restart or container death)"
	}
	if haveExit {
		return exitHeadline(exitCode, errLine)
	}
	if errLine != "" {
		return errLine
	}
	return genericFailureHeadline
}

// hookHeadline maps the hook event's error type; "" means no hook signal.
func hookHeadline(errorType string) string {
	switch {
	case errorType == "rate_limit":
		return "Claude hit a rate limit and stopped"
	case errorType != "":
		return "Claude stopped with error: " + errorType
	}
	return ""
}

// exitHeadline maps a known exit code to its meaning, falling back to the
// error-line heuristic for ordinary nonzero exits.
func exitHeadline(code int, errLine string) string {
	switch code {
	case 0:
		if errLine != "" {
			return "agent finished without posting the stage handoff: " + errLine
		}
		return genericFailureHeadline
	case 124:
		return "agent process timed out (exit 124)"
	case 137:
		return "agent container process was killed (exit 137 — OOM or docker kill)"
	case 139:
		return "agent process crashed (segfault, exit 139)"
	}
	if errLine != "" {
		return fmt.Sprintf("claude exited with code %d: %s", code, errLine)
	}
	return fmt.Sprintf("claude exited with code %d before completing the stage", code)
}

// detailFor composes the markdown detail block: run identity, duration, exit
// code, then the sanitized last lines of output inside a ~~~ fence (tildes, not
// backticks, so log lines containing ``` cannot break out of the fence).
func detailFor(exe *Execution, outcome *OutcomeRecord, exitCode int, haveExit bool, scan []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "agent: %s\n", exe.Launch.Agent)
	if d := runDuration(exe, outcome); d != "" {
		fmt.Fprintf(&b, "duration: %s\n", d)
	}
	if haveExit {
		fmt.Fprintf(&b, "exit code: %d\n", exitCode)
	}
	tail := fenceSafeTail(scan)
	if len(tail) > 0 {
		b.WriteString("\nlast output:\n~~~\n")
		for _, l := range tail {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		b.WriteString("~~~")
	}
	return strings.TrimSpace(b.String())
}

// fenceSafeTail sanitizes and caps the quoted tail: last diagnoseTailLines
// lines, each length-capped, secret-scrubbed, and never itself a ~ fence line.
func fenceSafeTail(scan []string) []string {
	if len(scan) > diagnoseTailLines {
		scan = scan[len(scan)-diagnoseTailLines:]
	}
	out := make([]string, 0, len(scan))
	for _, l := range scan {
		l = strings.TrimRight(stripANSI(l), " \t")
		if t := strings.TrimSpace(l); t != "" && strings.Trim(t, "~") == "" {
			continue
		}
		out = append(out, truncateRunes(sanitizeForTracker(l), diagnoseMaxLineLen))
	}
	return out
}

// runDuration prefers the recorded outcome duration; a run without an outcome
// yet is measured from its launch. Second precision — this is for humans.
func runDuration(exe *Execution, outcome *OutcomeRecord) string {
	if outcome != nil && outcome.DurationMs > 0 {
		return (time.Duration(outcome.DurationMs) * time.Millisecond).Truncate(time.Second).String()
	}
	if !exe.Launch.StartedAt.IsZero() {
		return time.Since(exe.Launch.StartedAt).Truncate(time.Second).String()
	}
	return ""
}

// secretShapeRes match well-known token formats regardless of where they came
// from. The marker body lands on the tracker — public relative to the host —
// so anything token-shaped is scrubbed even if it is not one of ours.
var secretShapeRes = []*regexp.Regexp{
	regexp.MustCompile(`gh[opsru]_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`xox[a-z]-[A-Za-z0-9-]{8,}`),
	regexp.MustCompile(`lin_api_[A-Za-z0-9]{8,}`),
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`glpat-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
}

// isSecretEnvName marks env vars whose VALUES must never reach a tracker
// comment; it deliberately mirrors the <TRACKER>_<NAME>_TOKEN / _KEY
// convention the daemon documents. Matching whole underscore-segments (not
// substrings) keeps PATH out while catching GITHUB_PAT.
func isSecretEnvName(name string) bool {
	for seg := range strings.SplitSeq(strings.ToUpper(name), "_") {
		switch seg {
		case "TOKEN", "SECRET", "KEY", "PASSWORD", "PASSWD", "PAT", "CREDENTIAL", "CREDENTIALS", "APIKEY":
			return true
		}
	}
	return false
}

// minSecretEnvLen keeps trivially short values (e.g. KEY=1) out of the
// redaction list — replacing those would shred ordinary log text.
const minSecretEnvLen = 8

var (
	secretEnvOnce   sync.Once
	secretEnvValues []string
)

// secretEnvList caches the env-derived redaction list; the daemon's environ is
// stable for the process lifetime.
func secretEnvList() []string {
	secretEnvOnce.Do(func() { secretEnvValues = collectSecretEnv(os.Environ()) })
	return secretEnvValues
}

// collectSecretEnv extracts the values of secret-named env vars from environ.
func collectSecretEnv(environ []string) []string {
	var vals []string
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || len(val) < minSecretEnvLen {
			continue
		}
		if isSecretEnvName(name) {
			vals = append(vals, val)
		}
	}
	return vals
}

// sanitizeForTracker scrubs a line for posting to a tracker comment.
func sanitizeForTracker(s string) string {
	return sanitizeText(s, secretEnvList())
}

// sanitizeText replaces token-shaped strings and known secret values with
// [redacted] and de-identifies the home directory.
func sanitizeText(s string, secrets []string) string {
	for _, re := range secretShapeRes {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	for _, sec := range secrets {
		s = strings.ReplaceAll(s, sec, "[redacted]")
	}
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		s = strings.ReplaceAll(s, home, "~")
	}
	return s
}

// truncateRunes caps s at max runes, marking the cut with an ellipsis.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}
