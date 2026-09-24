package cmddaemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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

// A fast tier that runs to completion and exits clean is a pass.
func TestFastTierRunner_cleanExitPasses(t *testing.T) {
	t.Setenv("PATH", writeFakeMake(t, "exit 0"))
	passed, err := fastTierRunner(context.Background(), t.TempDir())
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
	passed, err := fastTierRunner(context.Background(), t.TempDir())
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
	passed, err := fastTierRunner(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("a command that never started must surface as an error")
	}
	if passed {
		t.Fatal("a tooling failure must not report passed")
	}
}
