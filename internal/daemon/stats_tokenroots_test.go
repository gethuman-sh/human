package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude"
)

// writeTranscript writes one assistant usage line as a JSONL transcript under
// dir, mirroring the shape Claude records.
func writeTranscript(t *testing.T, dir, name string, ts time.Time, output int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o750))

	line, err := json.Marshal(map[string]any{
		"type":      "assistant",
		"timestamp": ts.Format(time.RFC3339),
		"message": map[string]any{
			"model": "claude-opus-4-5",
			"usage": map[string]int{
				"input_tokens":                10,
				"output_tokens":               output,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     0,
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), append(line, '\n'), 0o600))
}

// newProjectDir creates a registrable project directory holding a .humanconfig.
func newProjectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig"), []byte("project: test\n"), 0o600))
	return dir
}

// TestDefaultTokenScan_readsHostAndAgentTrees is the regression guard for
// SC-3581: the panel used to report the operator's own sessions as if they were
// the whole machine, leaving out everything the agents spent.
func TestDefaultTokenScan_readsHostAndAgentTrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now().UTC()

	writeTranscript(t, filepath.Join(home, ".claude", "projects", "a"), "s.jsonl", now, 100)

	p := newProjectDir(t)
	writeTranscript(t, filepath.Join(p, ".devcontainer", "claude", "projects", "b"), "s.jsonl", now, 300)

	reg, err := NewProjectRegistry([]string{p})
	require.NoError(t, err)
	srv := &Server{Projects: reg}

	scan, err := defaultTokenScan(srv.registeredProjectDirs())(now.Add(-time.Hour), now, now)
	require.NoError(t, err)

	assert.Equal(t, 400, scan.WindowOutput, "host tree + agent tree")
}

// TestDefaultTokenScan_ignoresNonProjectFilesInClaudeDir proves the walk targets
// each root's projects/ subtree only — the directory above it holds the
// credential store, which this tool has no business opening.
func TestDefaultTokenScan_ignoresNonProjectFilesInClaudeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now().UTC()

	p := newProjectDir(t)
	claudeDir := filepath.Join(p, ".devcontainer", "claude")
	writeTranscript(t, claudeDir, "history.jsonl", now, 999)
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(`{"token":"secret"}`), 0o600))

	reg, err := NewProjectRegistry([]string{p})
	require.NoError(t, err)
	srv := &Server{Projects: reg}

	scan, err := defaultTokenScan(srv.registeredProjectDirs())(now.Add(-time.Hour), now, now)
	require.NoError(t, err)

	assert.Equal(t, 0, scan.WindowOutput, "only the projects/ subtree is walked")
}

func TestDefaultTokenScan_nilRegistryHostOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now().UTC()

	writeTranscript(t, filepath.Join(home, ".claude", "projects", "a"), "s.jsonl", now, 100)

	scan, err := defaultTokenScan(nil)(now.Add(-time.Hour), now, now)
	require.NoError(t, err)

	assert.Equal(t, 100, scan.WindowOutput)
}

// TestDefaultTokenScan_missingProjectDegrades holds the per-root degrade
// contract: a project directory that no longer exists contributes nothing
// rather than emptying the whole panel.
func TestDefaultTokenScan_missingProjectDegrades(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now().UTC()

	p := newProjectDir(t)
	writeTranscript(t, filepath.Join(p, ".devcontainer", "claude", "projects", "b"), "s.jsonl", now, 300)
	deleted := filepath.Join(t.TempDir(), "gone")

	reg, err := NewProjectRegistry([]string{p, deleted})
	require.NoError(t, err)
	srv := &Server{Projects: reg}

	scan, err := defaultTokenScan(srv.registeredProjectDirs())(now.Add(-time.Hour), now, now)
	require.NoError(t, err)

	assert.Equal(t, 300, scan.WindowOutput)
}

// TestRegisteredProjectDirs_nilRegistry guards the nil check the existing
// nil-Projects stats tests depend on to reach the production scan path.
func TestRegisteredProjectDirs_nilRegistry(t *testing.T) {
	assert.Nil(t, (&Server{}).registeredProjectDirs())
}

func TestRegisteredProjectDirs_listsEachProject(t *testing.T) {
	p1 := newProjectDir(t)
	p2 := newProjectDir(t)

	reg, err := NewProjectRegistry([]string{p1, p2})
	require.NoError(t, err)

	dirs := (&Server{Projects: reg}).registeredProjectDirs()
	assert.ElementsMatch(t, []string{p1, p2}, dirs)
}

// TestDefaultTokenScan_doesNotDoubleCountRepeatedProject is the AC3 regression:
// the scan has no per-message dedupe, so a tree reachable twice would turn an
// understated number into an overstated one.
func TestDefaultTokenScan_doesNotDoubleCountRepeatedProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now().UTC()

	p := newProjectDir(t)
	writeTranscript(t, filepath.Join(p, ".devcontainer", "claude", "projects", "b"), "s.jsonl", now, 300)

	once, err := defaultTokenScan([]string{p})(now.Add(-time.Hour), now, now)
	require.NoError(t, err)
	twice, err := defaultTokenScan([]string{p, p})(now.Add(-time.Hour), now, now)
	require.NoError(t, err)

	assert.Equal(t, 300, once.WindowOutput)
	assert.Equal(t, once.WindowOutput, twice.WindowOutput, "a doubly-registered project must not double-count")
}

// TestScanTokensCached_usesRegistryOnProductionPath proves the cached scan
// reaches the enumerator when no TokenScanner is injected, and that the TTL
// cache still serves the second call from memory (AC6).
func TestScanTokensCached_usesRegistryOnProductionPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now().UTC()

	writeTranscript(t, filepath.Join(home, ".claude", "projects", "a"), "s.jsonl", now, 100)
	p := newProjectDir(t)
	writeTranscript(t, filepath.Join(p, ".devcontainer", "claude", "projects", "b"), "s.jsonl", now, 300)

	reg, err := NewProjectRegistry([]string{p})
	require.NoError(t, err)
	srv := &Server{Projects: reg}

	first := srv.scanTokensCached(RangeDay, now.Add(-24*time.Hour), now, now)
	assert.Equal(t, 400, first.WindowOutput)

	// Remove the agent tree; within the TTL the cached scan is still served.
	require.NoError(t, os.RemoveAll(filepath.Join(p, ".devcontainer")))
	second := srv.scanTokensCached(RangeDay, now.Add(-24*time.Hour), now, now)
	assert.Equal(t, 400, second.WindowOutput, "same range within TTL is served from cache")
}

// TestDefaultTokenScan_reportsEveryModelInUse is AC7: the by-model panel must
// list every model actually running, not only the operator's own.
func TestDefaultTokenScan_reportsEveryModelInUse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now().UTC()

	writeTranscript(t, filepath.Join(home, ".claude", "projects", "a"), "s.jsonl", now, 100)

	p := newProjectDir(t)
	agentDir := filepath.Join(p, ".devcontainer", "claude", "projects", "b")
	require.NoError(t, os.MkdirAll(agentDir, 0o750))
	line, err := json.Marshal(map[string]any{
		"type":      "assistant",
		"timestamp": now.Format(time.RFC3339),
		"message": map[string]any{
			"model": "claude-haiku-4-5",
			"usage": map[string]int{"input_tokens": 5, "output_tokens": 50},
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "s.jsonl"), append(line, '\n'), 0o600))

	scan, err := defaultTokenScan([]string{p})(now.Add(-time.Hour), now, now)
	require.NoError(t, err)

	models := make([]string, 0, len(scan.ByModel))
	for _, m := range scan.ByModel {
		models = append(models, m.Model)
	}
	assert.ElementsMatch(t, []string{"opus 4.5", "haiku 4.5"}, models)
}

// TestTranscriptRootsFromRegistry_coversEachProject ties the daemon's dirs to
// the enumerator so a registry change cannot silently drop a tree.
func TestTranscriptRootsFromRegistry_coversEachProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p1 := newProjectDir(t)
	p2 := newProjectDir(t)

	reg, err := NewProjectRegistry([]string{p1, p2})
	require.NoError(t, err)

	roots := claude.TranscriptRoots((&Server{Projects: reg}).registeredProjectDirs())
	assert.Len(t, roots, 3, "host tree plus one agent tree per project")
}
