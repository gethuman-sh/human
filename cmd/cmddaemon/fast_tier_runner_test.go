package cmddaemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeFakeMake puts an executable named "make" on its own PATH entry so
// fastTierRunner's `make test` invocation resolves to a script this test
// controls rather than the real build. Returns the directory to prepend to
// PATH.
func writeFakeMake(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fastTierRunner shells out to a POSIX script; windows needs its own fixture")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "make")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil { //nolint:gosec // test fixture, fixed content
		t.Fatalf("writing fake make: %v", err)
	}
	return dir
}

// writeMakefileWithTestTarget gives a project dir a Makefile declaring a
// `test` target, so detectFastTierTestCommand picks `make test` the way it
// would on a real Make-based project.
func writeMakefileWithTestTarget(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test:\n\tgo test ./...\n"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("writing Makefile: %v", err)
	}
	return dir
}

// A fast tier that runs to completion and exits clean is a pass.
func TestFastTierRunner_cleanExitPasses(t *testing.T) {
	t.Setenv("PATH", writeFakeMake(t, "exit 0"))
	passed, err := fastTierRunner(context.Background(), writeMakefileWithTestTarget(t))
	if err != nil {
		t.Fatalf("unexpected tooling error: %v", err)
	}
	if !passed {
		t.Fatal("a clean exit must report passed")
	}
}

// A fast tier that runs to completion and exits non-zero is a genuine
// failure the caller routes to the deploy fixer — never a tooling error.
func TestFastTierRunner_nonZeroExitIsAFailureNotAnError(t *testing.T) {
	t.Setenv("PATH", writeFakeMake(t, "exit 1"))
	passed, err := fastTierRunner(context.Background(), writeMakefileWithTestTarget(t))
	if err != nil {
		t.Fatalf("a failing test run must not be reported as a tooling error: %v", err)
	}
	if passed {
		t.Fatal("a non-zero exit must not report passed")
	}
}

// No `make` on the host at all is a tooling failure, distinct from a failing
// test run: the caller must not treat it as a verdict on the code.
func TestFastTierRunner_missingToolingIsAnError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty: `make` resolves nowhere
	passed, err := fastTierRunner(context.Background(), writeMakefileWithTestTarget(t))
	if err == nil {
		t.Fatal("a command that never started must surface as an error")
	}
	if passed {
		t.Fatal("a tooling failure must not report passed")
	}
}

// `make` present on the host but the project's Makefile has no `test`
// target (or no Makefile, and no other recognised manifest) must be reported
// as a tooling condition, not as a fast-tier failure: `make test` on such a
// project exits non-zero ("No rule to make target 'test'") in exactly the
// shape of a genuine failing test run, and the two must not collapse
// (SC-5279).
func TestFastTierRunner_noTestRunnerIsNotAFailure(t *testing.T) {
	t.Setenv("PATH", writeFakeMake(t, "exit 2")) // real make's behaviour for a missing target
	dir := t.TempDir()                           // no Makefile, no manifest of any kind
	passed, err := fastTierRunner(context.Background(), dir)
	if err == nil {
		t.Fatal("no detectable test runner must surface as an error, not a failure verdict")
	}
	if passed {
		t.Fatal("no detectable test runner must not report passed")
	}
}

// A Makefile that exists but declares no `test` target must not trigger
// `make test` at all — detection must not run a command it cannot back.
func TestFastTierRunner_makefileWithoutTestTargetIsNotAFailure(t *testing.T) {
	t.Setenv("PATH", writeFakeMake(t, "exit 2"))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\tgo build ./...\n"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("writing Makefile: %v", err)
	}
	passed, err := fastTierRunner(context.Background(), dir)
	if err == nil {
		t.Fatal("a Makefile without a test target must surface as an error, not a failure verdict")
	}
	if passed {
		t.Fatal("a Makefile without a test target must not report passed")
	}
}

// A run cut off by the fast tier's own time budget must be reported as a
// tooling condition: the killed process unwraps to the same *exec.ExitError
// a genuine failure does, so the deadline is what must be checked first.
func TestFastTierRunner_timeBudgetExceededIsNotAFailure(t *testing.T) {
	// The fake `make` script's own PATH lookup for `sleep` needs the real
	// PATH behind it — a directory containing only the fake `make` leaves
	// `sleep` unresolved, and an unresolved command in a script without
	// `set -e` is silently skipped rather than blocking.
	t.Setenv("PATH", writeFakeMake(t, "sleep 5; exit 0")+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	passed, err := fastTierRunner(ctx, writeMakefileWithTestTarget(t))
	if err == nil {
		t.Fatal("a run killed by the time budget must surface as an error, not a failure verdict")
	}
	if passed {
		t.Fatal("a run killed by the time budget must not report passed")
	}
}

// detectFastTierTestCommand picks per-ecosystem tooling, in reading order,
// when no Makefile test target is present.
func TestDetectFastTierTestCommand_perEcosystem(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		wantCmd  string
	}{
		{"go", "go.mod", "go"},
		{"node", "package.json", "npm"},
		{"rust", "Cargo.toml", "cargo"},
		{"python-pyproject", "pyproject.toml", "pytest"},
		{"python-requirements", "requirements.txt", "pytest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.manifest), []byte(""), 0o644); err != nil { //nolint:gosec // test fixture
				t.Fatalf("writing %s: %v", tc.manifest, err)
			}
			cmdName, _, err := detectFastTierTestCommand(dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cmdName != tc.wantCmd {
				t.Fatalf("got %q, want %q", cmdName, tc.wantCmd)
			}
		})
	}
}

// A Makefile test target takes priority over any other manifest present,
// matching build-gate.md's probe order.
func TestDetectFastTierTestCommand_makefileTakesPriority(t *testing.T) {
	dir := writeMakefileWithTestTarget(t)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("writing go.mod: %v", err)
	}
	cmdName, args, err := detectFastTierTestCommand(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmdName != "make" || len(args) != 1 || args[0] != "test" {
		t.Fatalf("got (%q, %v), want (make, [test])", cmdName, args)
	}
}

// No manifest of any recognised kind is a tooling condition, reported as an
// error rather than defaulting to any one ecosystem's assumption.
func TestDetectFastTierTestCommand_noneFoundIsAnError(t *testing.T) {
	if _, _, err := detectFastTierTestCommand(t.TempDir()); err == nil {
		t.Fatal("an empty project dir must report an error, not a guessed command")
	}
}
