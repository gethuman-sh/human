package cmdusage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeDaemonInfo records a daemon.json under home listing the given project
// dirs, which is where `usage` learns about projects — it never forwards to the
// daemon, so it has no ProjectRegistry of its own.
func writeDaemonInfo(t *testing.T, home string, dirs ...string) {
	t.Helper()
	projects := make([]map[string]string, 0, len(dirs))
	for i, dir := range dirs {
		projects = append(projects, map[string]string{"name": "p" + string(rune('1'+i)), "dir": dir})
	}
	data, err := json.Marshal(map[string]any{"addr": "127.0.0.1:0", "projects": projects})
	require.NoError(t, err)

	dir := filepath.Join(home, ".human")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "daemon.json"), data, 0o600))
}

// writeTranscript writes one assistant usage line inside the current 5h window.
func writeTranscript(t *testing.T, dir string, ts time.Time, output int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o750))

	line, err := json.Marshal(map[string]any{
		"type":      "assistant",
		"timestamp": ts.Format(time.RFC3339),
		"message": map[string]any{
			"model": "claude-opus-4-5",
			"usage": map[string]int{"input_tokens": 10, "output_tokens": output},
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "s.jsonl"), append(line, '\n'), 0o600))
}

func TestRegisteredProjectDirs_readsDaemonInfo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := t.TempDir()
	writeDaemonInfo(t, home, p, "")

	assert.Equal(t, []string{p}, registeredProjectDirs(), "an empty dir contributes nothing")
}

func TestRegisteredProjectDirs_noInfoFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	assert.Nil(t, registeredProjectDirs(), "a machine where a daemon never ran is a smaller answer, not an error")
}

func TestLocalTranscriptRoots_includesRegisteredProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := t.TempDir()
	writeDaemonInfo(t, home, p)

	roots := localTranscriptRoots()

	var found bool
	for _, r := range roots {
		if strings.HasPrefix(r, p) {
			found = true
		}
	}
	assert.True(t, found, "the registered project's agent tree is missing from %v", roots)
}

func TestPrintLocalUsage_sumsAcrossRoots(t *testing.T) {
	now := time.Now().UTC()
	rootA, rootB := t.TempDir(), t.TempDir()
	writeTranscript(t, filepath.Join(rootA, "a"), now, 100)
	writeTranscript(t, filepath.Join(rootB, "b"), now, 300)

	buf := &bytes.Buffer{}
	require.NoError(t, printLocalUsage(buf, []string{rootA, rootB}, now))

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
	assert.Contains(t, out, "opus", "both roots' model rows must be reported")
	assert.Contains(t, out, "out: 400", "the window total spans every root")
}

func TestPrintLocalUsage_noRoots(t *testing.T) {
	buf := &bytes.Buffer{}
	require.NoError(t, printLocalUsage(buf, nil, fixedTime()))

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
	assert.NotContains(t, out, "opus")
}
