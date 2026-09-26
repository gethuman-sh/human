package gotoolchain

import (
	"context"
	"errors"
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

func TestGoDirective_ignoresACommentedDirective(t *testing.T) {
	// "go 1.26.6 // comment" splits into 4 fields, not the 2 GoDirective
	// requires, so it no-ops rather than misreading a directive that isn't
	// there in the shape expected.
	require.Equal(t, "", GoDirective([]byte("module x\n\ngo 1.26.6 // comment\n")))
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
}
