package gotoolchain

import (
	"context"
	"errors"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoDirective_readsTheDirectiveOnly(t *testing.T) {
	gomod := "module x\n\ngo 1.26.6\n\ntoolchain go1.26.9\n\nrequire (\n\tgo.uber.org/zap v1\n)\n"
	require.Equal(t, "1.26.6", GoDirective([]byte(gomod)))
}

func TestGoDirective_absent(t *testing.T) {
	require.Equal(t, "", GoDirective([]byte("module x\n")))
}

func TestGoDirective_stripsATrailingComment(t *testing.T) {
	// A commented directive is still a directive — go.mod is free to
	// annotate it, and reading "" here would silently disable both the
	// wizard's pin and the container-start check for a project that does
	// this (SC-5879 review).
	require.Equal(t, "1.26.6", GoDirective([]byte("module x\n\ngo 1.26.6 // pinned by SC-5879\n")))
}

func TestGoDirective_aWhollyMalformedLineIsIgnored(t *testing.T) {
	// Not every line starting with "go" is the directive — e.g. a stray
	// "go build" left in go.mod by hand-editing. Only the exact `go <ver>`
	// shape (after stripping any comment) is read.
	require.Equal(t, "", GoDirective([]byte("module x\n\ngo build ./...\n")))
}

func TestRequirement_noGoMod(t *testing.T) {
	dir := t.TempDir()
	v, ok, err := Requirement(dir)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, "", v)
}

func TestRequirement_readsGoMod(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.26.6\n"), 0o600))
	v, ok, err := Requirement(dir)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "1.26.6", v)
}

// A go.mod that exists but names no readable directive is a different fault
// than no go.mod at all — the caller must be able to tell "nothing here" from
// "something's wrong with what's here" rather than reporting both as "no
// go.mod in <dir>" (SC-5879 review).
func TestRequirement_noDirective(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo build ./...\n"), 0o600))
	v, ok, err := Requirement(dir)
	require.ErrorIs(t, err, ErrNoDirective)
	require.False(t, ok)
	require.Equal(t, "", v)
}

func TestCheck_satisfied(t *testing.T) {
	for _, tc := range []struct{ required, installed string }{
		{"1.26.6", "1.26.6"},
		{"1.26.6", "1.27.0"},
		{"1.26", "1.26.5"},
	} {
		require.NoErrorf(t, Check(tc.required, tc.installed), "required=%s installed=%s", tc.required, tc.installed)
	}
}

func TestCheck_behindNamesBothVersions(t *testing.T) {
	err := Check("1.26.6", "1.26.5")
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrUndecidable))
	require.Contains(t, err.Error(), "1.26.6")
	require.Contains(t, err.Error(), "1.26.5")
}

func TestCheck_undecidable(t *testing.T) {
	for _, tc := range []struct{ required, installed string }{
		{"1.26.6", ""},
		{"1.26.6", "devel +abc"},
		{"", "1.26.5"},
	} {
		err := Check(tc.required, tc.installed)
		require.Errorf(t, err, "required=%q installed=%q", tc.required, tc.installed)
		require.Truef(t, errors.Is(err, ErrUndecidable), "required=%q installed=%q", tc.required, tc.installed)
	}
}

func TestOSProber_Installed(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	out, err := OSProber{}.Installed(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, out)
	// A prober returning garbage Check would call undecidable must not pass
	// silently — assert it is a real version, not merely non-empty.
	require.True(t, version.IsValid("go"+out), "Installed returned %q, not a valid version", out)
}
