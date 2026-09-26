package cmddoctor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

// stubProber returns a fixed version, or fails when err is set.
type stubProber struct {
	version string
	err     error
}

func (s stubProber) Installed(_ context.Context) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.version, nil
}

func writeGoMod(t *testing.T, dir, directive string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo "+directive+"\n"), 0o600))
}

func TestRunToolchainCheck_behind(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir, "1.26.6")
	var buf bytes.Buffer

	err := runToolchainCheck(&buf, dir, stubProber{version: "1.26.5"})

	require.Error(t, err)
	out := buf.String()
	require.Contains(t, out, "1.26.6")
	require.Contains(t, out, "1.26.5")
	require.Contains(t, out, `pin "version": "1.26.6"`)
	require.True(t, hasPrefixLine(out, "✗"))
}

func TestRunToolchainCheck_satisfied(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir, "1.26.6")
	var buf bytes.Buffer

	err := runToolchainCheck(&buf, dir, stubProber{version: "1.26.6"})

	require.NoError(t, err)
	out := buf.String()
	require.Contains(t, out, "1.26.6")
	require.True(t, hasPrefixLine(out, "✓"))
}

func TestRunToolchainCheck_noGoMod(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	err := runToolchainCheck(&buf, dir, stubProber{version: "1.26.6"})

	require.NoError(t, err)
	require.Contains(t, buf.String(), "nothing to check")
}

func TestRunToolchainCheck_proberFails(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir, "1.26.6")
	var buf bytes.Buffer

	err := runToolchainCheck(&buf, dir, stubProber{err: errors.WithDetails("go not on PATH")})

	require.NoError(t, err)
	out := buf.String()
	require.Contains(t, out, "1.26.6")
	require.Contains(t, out, "could not be asked")
}

func TestRunToolchainCheck_undecidable(t *testing.T) {
	dir := t.TempDir()
	writeGoMod(t, dir, "1.26.6")
	var buf bytes.Buffer

	err := runToolchainCheck(&buf, dir, stubProber{version: "devel"})

	require.NoError(t, err)
	require.Contains(t, buf.String(), "not comparable")
}

func TestBuildDoctorCmd_hasToolchainSubcommand(t *testing.T) {
	cmd := BuildDoctorCmd()
	found := false
	for _, c := range cmd.Commands() {
		if c.Name() == "toolchain" {
			found = true
		}
	}
	require.True(t, found, "doctor must carry a toolchain subcommand")
}

func hasPrefixLine(out, prefix string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
