package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCfg(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, ".humanconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// sectionKeys is the top-level keys of a config, so a test can assert a section
// is gone without matching the word inside a comment that legitimately names it.
func sectionKeys(config string) []string {
	var keys []string
	for _, line := range strings.Split(config, "\n") {
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "#") {
			continue
		}
		if key, _, ok := strings.Cut(line, ":"); ok {
			keys = append(keys, key)
		}
	}
	return keys
}

func readCfg(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test-owned temp path
	require.NoError(t, err)
	return string(data)
}

// The common case: an entry that was tracker and forge at once becomes the two
// things it was doing the work of. The tracker entry stays exactly where it was.
func TestMigrateForges_addsAForgeBesideTheTracker(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, "githubs:\n  - name: human\n    token: gh://token\n")

	result, err := MigrateForges(dir, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"human"}, result.Added)
	assert.Empty(t, result.Moved)

	out := readCfg(t, path)
	assert.Contains(t, out, "forges:")
	assert.Contains(t, out, "githubs:", "the tracker entry stays — it is a tracker")
	assert.Contains(t, out, "gh://token")
}

// A vault reference migrates as a reference. Resolving it would write a live
// credential into a config file, which is a leak with a long half-life.
func TestMigrateForges_neverResolvesASecret(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, "githubs:\n  - name: human\n    token: 1pw://Private/GitHub/token\n")

	_, err := MigrateForges(dir, false)
	require.NoError(t, err)
	assert.Contains(t, readCfg(t, path), "1pw://Private/GitHub/token")
}

// role: forge was never a tracker, so it moves rather than being copied. Leaving
// it behind would leave a config that fails to load — a migration that reports
// success and breaks the tool is worse than no migration.
func TestMigrateForges_movesARetiredForgeRoleEntry(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, "githubs:\n  - name: prs\n    token: ghp_x\n    role: forge\n")

	result, err := MigrateForges(dir, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"prs"}, result.Moved)

	out := readCfg(t, path)
	assert.Contains(t, out, "forges:")
	assert.NotContains(t, sectionKeys(out), "githubs",
		"an emptied section is removed, not left looking configured")
	assert.NotContains(t, out, "role: forge")
}

// A tracker that declared a tracker role never opened pull requests, so it has
// nothing to migrate and must be left alone.
func TestMigrateForges_leavesADeclaredTrackerAlone(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "githubs:\n  - name: work\n    token: ghp_x\n    role: pm\n")

	result, err := MigrateForges(dir, false)
	require.NoError(t, err)
	assert.True(t, result.Empty())
}

func TestMigrateForges_isIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, "githubs:\n  - name: human\n    token: gh://token\n")

	_, err := MigrateForges(dir, false)
	require.NoError(t, err)
	first := readCfg(t, path)

	result, err := MigrateForges(dir, false)
	require.NoError(t, err)
	assert.True(t, result.Empty(), "a second run has nothing to add")
	assert.Equal(t, first, readCfg(t, path))
}

func TestMigrateForges_dryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, "githubs:\n  - name: human\n    token: gh://token\n")
	before := readCfg(t, path)

	result, err := MigrateForges(dir, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"human"}, result.Added)
	assert.Equal(t, before, readCfg(t, path), "a dry run must not touch the file")
}

// Comments and unrelated sections survive: this rewrites someone's hand-written
// config, and losing their notes to a migration is its own kind of damage.
func TestMigrateForges_preservesCommentsAndOtherSections(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, `# my project config
project: cli

githubs:
  # the token lives in 1Password
  - name: human
    token: gh://token

shortcuts:
  - name: board
    role: pm
`)

	_, err := MigrateForges(dir, false)
	require.NoError(t, err)

	out := readCfg(t, path)
	assert.Contains(t, out, "# my project config")
	assert.Contains(t, out, "# the token lives in 1Password")
	assert.Contains(t, out, "role: pm")
	assert.Contains(t, out, "forges:")
}

// A token-less entry has nothing to carry over, so no forge entry is written
// for it — a forge with no credential could not open a pull request anyway.
func TestMigrateForges_skipsATokenlessEntry(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "githubs:\n  - name: human\n")

	result, err := MigrateForges(dir, false)
	require.NoError(t, err)
	assert.True(t, result.Empty())
}

func TestMigrateForges_noConfigFile(t *testing.T) {
	_, err := MigrateForges(t.TempDir(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no .humanconfig")
}
