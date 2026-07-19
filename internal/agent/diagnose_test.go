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

func TestDiagnoseFailure_HookRateLimitBeatsArtifacts(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-1-planning", "some output\n"+execExitTrailerPrefix+"137\n",
		&OutcomeRecord{Reason: "reaped", EndedAt: time.Now()})
	d := DiagnoseFailure("board-SC-1-planning", "rate_limit")
	if d.Headline != "Claude hit a rate limit and stopped" {
		t.Fatalf("headline = %q", d.Headline)
	}
}

func TestDiagnoseFailure_OtherHookErrorType(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-1-planning", "", nil)
	d := DiagnoseFailure("board-SC-1-planning", "max_tokens")
	if d.Headline != "Claude stopped with error: max_tokens" {
		t.Fatalf("headline = %q", d.Headline)
	}
}

func TestDiagnoseFailure_Reaped(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-2-implementation", "working...\n",
		&OutcomeRecord{Reason: "reaped", DurationMs: 252_000, EndedAt: time.Now()})
	d := DiagnoseFailure("board-SC-2-implementation", "")
	if !strings.Contains(d.Headline, "reaped") {
		t.Fatalf("headline = %q", d.Headline)
	}
	if !strings.Contains(d.Detail, "agent: board-SC-2-implementation") {
		t.Fatalf("detail missing agent line: %q", d.Detail)
	}
	if !strings.Contains(d.Detail, "duration: 4m12s") {
		t.Fatalf("detail missing duration: %q", d.Detail)
	}
}

func TestDiagnoseFailure_Exit137(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-3-review", "reviewing\n\n"+execExitTrailerPrefix+"137\n", nil)
	d := DiagnoseFailure("board-SC-3-review", "")
	if !strings.Contains(d.Headline, "exit 137") || !strings.Contains(d.Headline, "killed") {
		t.Fatalf("headline = %q", d.Headline)
	}
	if !strings.Contains(d.Detail, "exit code: 137") {
		t.Fatalf("detail missing exit code: %q", d.Detail)
	}
}

func TestDiagnoseFailure_ExitOneWithAPIErrorLine(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	out := "Running /human-autofix SC-1 --board\n" +
		"API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n" +
		execExitTrailerPrefix + "1\n"
	newRunFixture(t, "board-SC-4-implementation", out, nil)
	d := DiagnoseFailure("board-SC-4-implementation", "")
	want := "claude exited with code 1: API Error: 529"
	if !strings.HasPrefix(d.Headline, want) {
		t.Fatalf("headline = %q, want prefix %q", d.Headline, want)
	}
	if !strings.Contains(d.Detail, "last output:\n~~~\n") || !strings.Contains(d.Detail, "overloaded_error") {
		t.Fatalf("detail missing fenced tail: %q", d.Detail)
	}
}

func TestDiagnoseFailure_ExitZeroWithErrorLine(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	out := "Error: no plan comment found on ticket\n" + execExitTrailerPrefix + "0\n"
	newRunFixture(t, "board-SC-5-planning", out, nil)
	d := DiagnoseFailure("board-SC-5-planning", "")
	if !strings.HasPrefix(d.Headline, "agent finished without posting the stage handoff: Error: no plan comment") {
		t.Fatalf("headline = %q", d.Headline)
	}
}

func TestDiagnoseFailure_ExitZeroNoErrorLineIsGeneric(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	newRunFixture(t, "board-SC-6-planning", "all quiet\n"+execExitTrailerPrefix+"0\n", nil)
	d := DiagnoseFailure("board-SC-6-planning", "")
	if d.Headline != genericFailureHeadline {
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
	for i := 0; i < 500; i++ {
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

func TestDiagnoseFailure_HeadlineTruncated(t *testing.T) {
	withLogRoot(t)
	withInstantDiagnosis(t)
	long := "Error: " + strings.Repeat("x", 5000)
	newRunFixture(t, "board-SC-9-planning", long+"\n"+execExitTrailerPrefix+"1\n", nil)
	d := DiagnoseFailure("board-SC-9-planning", "")
	if l := len([]rune(d.Headline)); l > diagnoseMaxHeadline {
		t.Fatalf("headline length %d exceeds cap", l)
	}
	if !strings.HasSuffix(d.Headline, "…") {
		t.Fatalf("truncated headline missing ellipsis: %q", d.Headline)
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

func TestCollectSecretEnv(t *testing.T) {
	environ := []string{
		"SHORTCUT_HUMAN_TOKEN=supersecretvalue",
		"JIRA_X_KEY=alsosecret99",
		"MY_PASSWORD=hunter2hunter2",
		"SHORT_KEY=tiny",      // below min length: kept out
		"HOME=/home/somebody", // not secret-named
		"PATH=/usr/bin:/bin",  // PAT is segment-matched: PATH stays out
		"NOEQUALS",            // malformed
	}
	vals := collectSecretEnv(environ)
	for _, want := range []string{"supersecretvalue", "alsosecret99", "hunter2hunter2"} {
		found := false
		for _, v := range vals {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %q in %v", want, vals)
		}
	}
	for _, v := range vals {
		if v == "tiny" {
			t.Fatal("short value must not be collected")
		}
		if v == "/usr/bin:/bin" {
			t.Fatal("PATH value must not be collected")
		}
	}
}

func TestSanitizeText(t *testing.T) {
	got := sanitizeText("push ghp_abcdefghijklmnopqrstu12 with Bearer abc.def.ghi and s3cr3tenvva1", []string{"s3cr3tenvva1"})
	if strings.Contains(got, "ghp_") || strings.Contains(got, "abc.def.ghi") || strings.Contains(got, "s3cr3tenvva1") {
		t.Fatalf("secrets survived sanitization: %q", got)
	}
	if strings.Count(got, "[redacted]") != 3 {
		t.Fatalf("want 3 redactions, got %q", got)
	}
}
