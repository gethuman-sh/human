package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withInstantDiagnosis removes the artifact-availability wait so tests never
// sleep.
func withInstantDiagnosis(t *testing.T) {
	t.Helper()
	oldStep, oldTries := diagnoseWaitStep, diagnoseWaitTries
	diagnoseWaitStep, diagnoseWaitTries = 0, 1
	t.Cleanup(func() { diagnoseWaitStep, diagnoseWaitTries = oldStep, oldTries })
}

// newRunFixture creates an execution for agentName with the given output.log
// content ("" = no log) and optional outcome.
func newRunFixture(t *testing.T, agentName, output string, outcome *OutcomeRecord) *Execution {
	t.Helper()
	exe, err := NewExecution(LaunchRecord{
		ID: newExecID(), Agent: agentName, StartedAt: time.Now().Add(-90 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if output != "" {
		if err := os.WriteFile(filepath.Join(exe.Dir(), "output.log"), []byte(output), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if outcome != nil {
		if err := exe.RecordOutcome(*outcome); err != nil {
			t.Fatal(err)
		}
	}
	return exe
}

// SC-3024: the hook's own errorType still beats artifact inference, but the
// headline is now a status ("the daemon is handling it"), never Claude/API
// vocabulary — rate_limit is unavailability, classified upstream by the
// daemon's classifyUnavailability before this ordinary path is reached for
// most calls; this is what a bare invocation (e.g. resumeTimeFromDiagnosis's
// scan) still reads.
func TestDiagnoseFailure_HookRateLimitBeatsArtifacts(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-1-planning", "some output\n"+execExitTrailerPrefix+"137\n",
		&OutcomeRecord{Reason: "reaped", EndedAt: time.Now()})
	d := DiagnoseFailure("board-SC-1-planning", "rate_limit")
	if d.Headline != "the run stopped at the model API — the daemon is handling it" {
		t.Fatalf("headline = %q", d.Headline)
	}
}

func TestDiagnoseFailure_OtherHookErrorType(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-1-planning", "", nil)
	d := DiagnoseFailure("board-SC-1-planning", "max_tokens")
	if d.Headline != "the run stopped at the model boundary; check the evidence below, then Retry" {
		t.Fatalf("headline = %q", d.Headline)
	}
}

// SC-3024: a reaped run's headline is a neutral status — never "reaped"
// vocabulary — but the detail evidence (agent/duration) is unchanged.
func TestDiagnoseFailure_Reaped(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-2-implementation", "working...\n",
		&OutcomeRecord{Reason: "reaped", DurationMs: 252_000, EndedAt: time.Now()})
	d := DiagnoseFailure("board-SC-2-implementation", "")
	want := "the run stopped before finishing this stage — check the evidence below, then Retry"
	if d.Headline != want {
		t.Fatalf("headline = %q, want %q", d.Headline, want)
	}
	if strings.Contains(d.Headline, "reaped") {
		t.Fatalf("headline must never say 'reaped': %q", d.Headline)
	}
	if !strings.Contains(d.Detail, "agent: board-SC-2-implementation") {
		t.Fatalf("detail missing agent line: %q", d.Detail)
	}
	if !strings.Contains(d.Detail, "duration: 4m12s") {
		t.Fatalf("detail missing duration: %q", d.Detail)
	}
}

// A run stamped "reaped" by the zombie sweep but carrying a recorded clean exit
// (code 0) self-terminated normally — it must not be reported as a crash/kill.
func TestHeadlineFor_ReapedButCleanExit(t *testing.T) {
	got := headlineFor("", true, 0, true, "")
	want := "agent finished without posting the stage handoff"
	if got != want {
		t.Fatalf("headline = %q, want %q", got, want)
	}
}

// A reap still beats a nonzero (killed) exit code — the reaped process's code
// is the killer's, not the cause — but the headline says so as a status, never
// "reaped"/crashed/killed vocabulary (SC-3024): it reads identically to the
// ordinary nonzero-exit status, since the exit code itself is not the cause
// either and lives in the Detail evidence instead.
func TestHeadlineFor_ReapedNonzeroExitStaysReaped(t *testing.T) {
	want := "the run stopped before finishing this stage — check the evidence below, then Retry"
	if got := headlineFor("", true, 137, true, ""); got != want {
		t.Fatalf("headline = %q, want %q", got, want)
	}
	if strings.Contains(headlineFor("", true, 137, true, ""), "reaped") {
		t.Fatalf("headline must never say 'reaped'")
	}
}

// End-to-end: a reaped outcome plus a clean-exit trailer in output.log resolves
// to the handoff-missing headline, never the reaped-crash wording.
func TestDiagnoseFailure_ReapedWithCleanExitTrailer(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-878-planning", "all quiet\n"+execExitTrailerPrefix+"0\n",
		&OutcomeRecord{Reason: "reaped", EndedAt: time.Now()})
	d := DiagnoseFailure("board-SC-878-planning", "")
	if d.Headline != "agent finished without posting the stage handoff" {
		t.Fatalf("headline = %q", d.Headline)
	}
	if strings.Contains(d.Headline, "reaped") {
		t.Fatalf("headline unexpectedly reaped: %q", d.Headline)
	}
}

// SC-3024: the exit code is Detail evidence only — the headline is a neutral
// status with no "exit 137"/"killed"/OOM/docker vocabulary (a guessed-at cause
// the reader cannot act on directly).
func TestDiagnoseFailure_Exit137(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-3-review", "reviewing\n\n"+execExitTrailerPrefix+"137\n", nil)
	d := DiagnoseFailure("board-SC-3-review", "")
	want := "the run stopped before finishing this stage — check the evidence below, then Retry"
	if d.Headline != want {
		t.Fatalf("headline = %q, want %q", d.Headline, want)
	}
	if strings.Contains(d.Headline, "137") || strings.Contains(d.Headline, "killed") {
		t.Fatalf("headline must not carry the exit code or guessed-at cause: %q", d.Headline)
	}
	if !strings.Contains(d.Detail, "exit code: 137") {
		t.Fatalf("detail missing exit code: %q", d.Detail)
	}
}

// SC-3024: the API error line is Detail-only evidence (the fenced last-output
// block); the headline is the same neutral nonzero-exit status regardless of
// the exit code or what was logged.
func TestDiagnoseFailure_ExitOneWithAPIErrorLine(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	out := "Running /human-autofix SC-1 --board\n" +
		"API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n" +
		execExitTrailerPrefix + "1\n"
	newRunFixture(t, "board-SC-4-implementation", out, nil)
	d := DiagnoseFailure("board-SC-4-implementation", "")
	want := "the run stopped before finishing this stage — check the evidence below, then Retry"
	if d.Headline != want {
		t.Fatalf("headline = %q, want %q", d.Headline, want)
	}
	if !strings.Contains(d.Detail, "last output:\n~~~\n") || !strings.Contains(d.Detail, "overloaded_error") {
		t.Fatalf("detail missing fenced tail: %q", d.Detail)
	}
}

// SC-3024: a clean exit-0 with something logged is still split out from the
// bare missing-handoff case, but the headline no longer echoes the raw log
// line — it names the situation and the recovery gesture; the line itself is
// Detail-only evidence.
func TestDiagnoseFailure_ExitZeroWithErrorLine(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	out := "Error: no plan comment found on ticket\n" + execExitTrailerPrefix + "0\n"
	newRunFixture(t, "board-SC-5-planning", out, nil)
	d := DiagnoseFailure("board-SC-5-planning", "")
	want := "the run finished without handing off the stage — check the evidence below, then re-run this stage."
	if d.Headline != want {
		t.Fatalf("headline = %q, want %q", d.Headline, want)
	}
	if !strings.Contains(d.Detail, "no plan comment found on ticket") {
		t.Fatalf("detail must still carry the logged line as evidence: %q", d.Detail)
	}
}

func TestDiagnoseFailure_ExitZeroNoErrorLineIsHandoffMissing(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-6-planning", "all quiet\n"+execExitTrailerPrefix+"0\n", nil)
	d := DiagnoseFailure("board-SC-6-planning", "")
	if d.Headline != "agent finished without posting the stage handoff" {
		t.Fatalf("headline = %q", d.Headline)
	}
}

func TestDiagnoseFailure_NoExecutionFallsBackToGeneric(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	d := DiagnoseFailure("board-SC-7-planning", "")
	if d.Headline != genericFailureHeadline {
		t.Fatalf("headline = %q", d.Headline)
	}
	if d.Detail != "" {
		t.Fatalf("detail should be empty, got %q", d.Detail)
	}
}

func TestDiagnoseFailure_TailIsRedactedAndCapped(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	home, _ := os.UserHomeDir()
	var b strings.Builder
	for i := range 500 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	b.WriteString("pushing with ghp_abcdefghijklmnopqrstuv123456 done\n")
	b.WriteString("workdir " + home + "/.human/worktrees/x\n")
	b.WriteString(execExitTrailerPrefix + "1\n")
	newRunFixture(t, "board-SC-8-implementation", b.String(), nil)
	d := DiagnoseFailure("board-SC-8-implementation", "")
	if strings.Contains(d.Detail, "ghp_") {
		t.Fatalf("token leaked into detail: %q", d.Detail)
	}
	if !strings.Contains(d.Detail, "[redacted]") {
		t.Fatalf("expected redaction marker: %q", d.Detail)
	}
	if home != "" && strings.Contains(d.Detail, home) {
		t.Fatalf("home dir leaked into detail: %q", d.Detail)
	}
	if got := strings.Count(extractFence(t, d.Detail), "\n"); got > diagnoseTailLines {
		t.Fatalf("fenced tail has %d lines, cap is %d", got, diagnoseTailLines)
	}
}

// extractFence returns the content between the ~~~ fences.
func extractFence(t *testing.T, detail string) string {
	t.Helper()
	parts := strings.Split(detail, "~~~")
	if len(parts) < 3 {
		t.Fatalf("detail has no closed fence: %q", detail)
	}
	return strings.Trim(parts[1], "\n")
}

// SC-3024: DiagnoseFailure no longer builds its headline out of a raw log
// line or an exit code (both are now Detail-only evidence), so end-to-end
// there is no longer a scenario that produces a headline anywhere near the
// cap — every headlineFor branch returns one of a handful of fixed, short
// status sentences. The cap itself (DiagnoseFailure still runs every headline
// through truncateRunes, defensively) is exercised directly here instead.
func TestDiagnoseFailure_HeadlineNeverExceedsCapEvenWithAHugeLogLine(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	long := "Error: " + strings.Repeat("x", 5000)
	newRunFixture(t, "board-SC-9-planning", long+"\n"+execExitTrailerPrefix+"1\n", nil)
	d := DiagnoseFailure("board-SC-9-planning", "")
	if l := len([]rune(d.Headline)); l > diagnoseMaxHeadline {
		t.Fatalf("headline length %d exceeds cap", l)
	}
	if strings.Contains(d.Headline, "xxxx") {
		t.Fatalf("a raw log line must never become the headline: %q", d.Headline)
	}
}

func TestTruncateRunes_CapsAndMarksTheCutWithAnEllipsis(t *testing.T) {
	long := strings.Repeat("x", diagnoseMaxHeadline+50)
	got := truncateRunes(long, diagnoseMaxHeadline)
	if l := len([]rune(got)); l != diagnoseMaxHeadline {
		t.Fatalf("truncated length = %d, want %d", l, diagnoseMaxHeadline)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated string missing ellipsis: %q", got)
	}
}

func TestDiagnoseFailure_FenceLinesDropped(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	out := "before\n~~~\n```\nafter Error: boom\n" + execExitTrailerPrefix + "1\n"
	newRunFixture(t, "board-SC-10-planning", out, nil)
	d := DiagnoseFailure("board-SC-10-planning", "")
	fence := extractFence(t, d.Detail)
	if strings.Contains(fence, "~~~") {
		t.Fatalf("tilde fence line must be dropped from tail: %q", fence)
	}
	if !strings.Contains(fence, "```") {
		t.Fatalf("backtick line is safe inside tilde fence and should stay: %q", fence)
	}
}

func TestDiagnoseFailure_TailKeepsRealMessagePastTrailerAndBlanks(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	var b strings.Builder
	b.WriteString("agent final message: work complete\n")
	// ANSI-only shutdown lines strip to empty and must not fill the tail window.
	for range 20 {
		b.WriteString("\x1b[0m\x1b[2K\n")
	}
	b.WriteString("\n\n")
	b.WriteString(execExitTrailerPrefix + "0\n")
	newRunFixture(t, "board-SC-1117-planning", b.String(), nil)
	d := DiagnoseFailure("board-SC-1117-planning", "")
	fence := extractFence(t, d.Detail)
	if !strings.Contains(fence, "agent final message: work complete") {
		t.Fatalf("fence must keep the agent's real final message: %q", fence)
	}
	if strings.Contains(fence, "exited with code") {
		t.Fatalf("exit trailer must be excluded from the quoted tail: %q", fence)
	}
	if !strings.Contains(d.Detail, "exit code: 0") {
		t.Fatalf("exit code must still be reported in its own field: %q", d.Detail)
	}
}

func TestParseExitTrailer(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		code  int
		ok    bool
	}{
		{"plain", []string{"x", execExitTrailerPrefix + "7"}, 7, true},
		{"ansi", []string{"\x1b[31m" + execExitTrailerPrefix + "1\x1b[0m"}, 1, true},
		{"absent", []string{"no trailer here"}, 0, false},
		{"garbage code", []string{execExitTrailerPrefix + "boom"}, 0, false},
		{"empty", nil, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, ok := parseExitTrailer(c.lines)
			if code != c.code || ok != c.ok {
				t.Fatalf("got (%d,%v), want (%d,%v)", code, ok, c.code, c.ok)
			}
		})
	}
}

func TestLastErrorLine(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"ansi colored", []string{"ok", "\x1b[31mError: boom\x1b[0m"}, "Error: boom"},
		{"panic", []string{"panic: nil deref", "goroutine 1"}, "panic: nil deref"},
		{"latest wins", []string{"Error: first", "Error: second"}, "Error: second"},
		{"trailer skipped", []string{"Error: real", execExitTrailerPrefix + "1"}, "Error: real"},
		{"oom", []string{"process was Killed by the kernel"}, "process was Killed by the kernel"},
		{"none", []string{"all good", "done"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lastErrorLine(c.lines); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
