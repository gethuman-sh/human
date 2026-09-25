package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The pre-push gate and the CI test job once carried two copies of
// `go list ./... | grep -v /cmd/`, and a third sat in `make coverage`. Nothing
// asserted they agreed or that the filter was justified, so 88 _test.go files
// under cmd/ ran in no gate that could fail a push or a merge, and two
// cmd/cmddaemon assertions stayed red on main for five days behind them
// (SC-3877).
//
// These tests guard the shape of the fix rather than its text: the package set
// is declared once, both Makefile gates take it from there, and CI invokes the
// Makefile instead of restating the command. A future narrowing is then one
// edit in one place — visible, and forced to explain itself here.

// readRepoFile reads a path relative to the module root. The root package is
// `main`, so a test in it runs with the module root as its working directory.
func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoErrorf(t, err, "reading %s from the module root", path)
	return string(b)
}

// recipeFor returns the recipe lines of a Makefile target: everything after the
// `target:` line that begins with a tab, stopping at the first line that does
// not. Read as text rather than executed because the assertion is about what the
// gate is DECLARED to run, which is exactly what drifted.
func recipeFor(t *testing.T, makefile, target string) string {
	t.Helper()
	lines := strings.Split(makefile, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, target+":") {
			continue
		}
		var recipe []string
		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(next, "\t") {
				break
			}
			recipe = append(recipe, next)
		}
		return strings.Join(recipe, "\n")
	}
	t.Fatalf("Makefile declares no target %q", target)
	return ""
}

func TestGatePackageSetIsDeclaredOnceAndUnfiltered(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")

	require.Contains(t, makefile, "\nGATE_PKGS ?= ./...\n",
		"the gate's package set must be declared once, as ./... — narrowing it hides whole "+
			"directories from every gate at once, which is what SC-3877 fixed")

	require.NotContains(t, makefile, "grep -v /cmd/",
		"no gate may filter cmd/ out of the test run: 88 test files and 17,510 lines of "+
			"test code under cmd/ ran nowhere for weeks behind exactly this expression (SC-3877)")

	for _, target := range []string{"check-test", "coverage"} {
		require.Containsf(t, recipeFor(t, makefile, target), "$(GATE_PKGS)",
			"the %s gate must take its package set from GATE_PKGS, not spell its own — "+
				"three independent spellings is how the cmd/ filter became invisible (SC-3877)", target)
	}
}

func TestCIRunsTheMakefileGateRatherThanRestatingIt(t *testing.T) {
	ci := readRepoFile(t, ".github/workflows/ci.yml")

	require.Contains(t, ci, "make coverage-check",
		"the CI test job must invoke the Makefile gate, so its package set and threshold are "+
			"the same bytes the pre-push gate uses (SC-3877)")

	require.NotContains(t, ci, "go list",
		"the CI test job must not build its own package list: a second copy of the expression "+
			"is free to drift from the Makefile's, and did (SC-3877)")
}
