package cmdconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/config"
)

func writeCfg(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o600))
}

func readCfg(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml")) // #nosec G304 -- test-owned temp path
	require.NoError(t, err)
	return string(data)
}

func TestRunMigrate_reportsWhatItMoved(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "githubs:\n  - name: human\n    token: gh://token\n")

	var buf bytes.Buffer
	require.NoError(t, RunMigrate(&buf, dir, false, false))

	out := buf.String()
	assert.Contains(t, out, `Moved githubs: entry "human" into forges:`)
	assert.Contains(t, out, "Updated:")
}

// The one thing the migration cannot know is whether that entry was in fact
// someone's issue tracker, so it must say so rather than decide in silence.
func TestRunMigrate_saysHowToKeepAGitHubTracker(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "githubs:\n  - name: human\n    token: gh://token\n")

	var buf bytes.Buffer
	require.NoError(t, RunMigrate(&buf, dir, false, false))
	assert.Contains(t, buf.String(), "role: pm")
}

// A dry run says "would", not "did". Reporting a write that did not happen is
// how someone concludes their config is fixed when it is not.
func TestRunMigrate_dryRunSaysWould(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "githubs:\n  - name: human\n    token: gh://token\n")

	var buf bytes.Buffer
	require.NoError(t, RunMigrate(&buf, dir, true, false))

	out := buf.String()
	assert.Contains(t, out, "Would move")
	assert.NotContains(t, out, "Updated:")
}

func TestRunMigrate_nothingToDo(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "shortcuts:\n  - name: board\n    token: x\n")

	var buf bytes.Buffer
	require.NoError(t, RunMigrate(&buf, dir, false, false))
	assert.Contains(t, buf.String(), "Nothing to migrate")
}

func TestRunMigrate_noConfig(t *testing.T) {
	var buf bytes.Buffer
	require.Error(t, RunMigrate(&buf, t.TempDir(), false, false))
}

// --group folds the per-vendor sections into one list, and says so.
func TestRunMigrate_groupReportsTheFold(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "jiras:\n  - name: work\n    token: t\nshortcuts:\n  - name: board\n    role: pm\n    token: t\n")

	var buf bytes.Buffer
	require.NoError(t, RunMigrate(&buf, dir, false, true))

	out := buf.String()
	assert.Contains(t, out, "Folded 2 entries into one trackers: list")
	assert.Contains(t, out, "jira")
	assert.Contains(t, out, "shortcut")
}

// Without the flag a working file is left in the shape its author chose: the
// grouping is a preference, and a repair must not restructure someone's config
// on the way past.
func TestRunMigrate_withoutGroupLeavesTheShapeAlone(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "jiras:\n  - name: work\n    token: t\n")
	before := readCfg(t, dir)

	var buf bytes.Buffer
	require.NoError(t, RunMigrate(&buf, dir, false, false))

	assert.Contains(t, buf.String(), "Nothing to migrate")
	assert.Equal(t, before, readCfg(t, dir))
}

func TestBuildConfigCmd_hasMigrateAndCheck(t *testing.T) {
	cmd := BuildConfigCmd()
	assert.Equal(t, "config", cmd.Use)

	var found []string
	for _, sub := range cmd.Commands() {
		found = append(found, sub.Use)
	}
	assert.Contains(t, found, "migrate")
	assert.Contains(t, found, "check")
}

// --- config check ---

// A clean config says so. Silence from a checker is indistinguishable from a
// checker that did not run.
func TestRunCheck_cleanConfigSaysSo(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "shortcuts:\n  - name: board\n    role: pm\n    token: t\nforges:\n  - name: prs\n    token: t\n")

	var buf bytes.Buffer
	require.NoError(t, RunCheck(&buf, dir, false))
	assert.Contains(t, buf.String(), "nothing to report")
}

// The state this project's own config was in: reported as an error, with the
// command that fixes it.
func TestRunCheck_reportsAHalfMigratedConfig(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "githubs:\n  - name: human\n    token: t\nforges:\n  - name: human\n    token: t\n")

	var buf bytes.Buffer
	err := RunCheck(&buf, dir, false)
	require.Error(t, err, "an error must fail the command so a script can gate on it")

	out := buf.String()
	assert.Contains(t, out, "half-migrated-github")
	assert.Contains(t, out, "fix: human config migrate")
}

// A warning is reported and does NOT fail: the config works, it just costs
// something its author probably did not intend.
func TestRunCheck_warningsDoNotFail(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "shortcuts:\n  - name: board\n    role: pm\n    token: t\n")

	var buf bytes.Buffer
	require.NoError(t, RunCheck(&buf, dir, false))
	assert.Contains(t, buf.String(), "no-forge")
}

func TestRunCheck_json(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "shortcuts:\n  - name: board\n    role: pm\n    token: t\n")

	var buf bytes.Buffer
	require.NoError(t, RunCheck(&buf, dir, true))

	var problems []config.Problem
	require.NoError(t, json.Unmarshal(buf.Bytes(), &problems))
	require.Len(t, problems, 1)
	assert.Equal(t, "no-forge", problems[0].Rule)
}

func TestRunCheck_missingConfigIsCheckable(t *testing.T) {
	var buf bytes.Buffer
	// No file at all is a config with no trackers and no forge, not a crash.
	require.NoError(t, RunCheck(&buf, t.TempDir(), false))
	assert.Contains(t, buf.String(), "no-forge")
}
