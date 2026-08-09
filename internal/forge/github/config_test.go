package github

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	require.NoError(t, os.Unsetenv(key))
}

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644))
}

func clearForgeEnv(t *testing.T, names ...string) {
	t.Helper()
	unsetEnv(t, "FORGE_URL")
	unsetEnv(t, "FORGE_TOKEN")
	for _, n := range names {
		unsetEnv(t, "FORGE_"+n+"_URL")
		unsetEnv(t, "FORGE_"+n+"_TOKEN")
	}
}

// Each forges: entry builds a code host and nothing else. A token-less entry is
// skipped rather than carried half-built.
func TestLoadInstances(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "forges:\n  - name: prs\n    url: https://api.github.com\n    token: ghp_forge\n  - name: broken\n")
	clearForgeEnv(t, "PRS", "BROKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1, "the token-less forge entry must be skipped")

	inst := instances[0]
	assert.Equal(t, "prs", inst.Name)
	assert.Equal(t, "github", inst.Kind)
	assert.NotNil(t, inst.Forge)
}

// Kind defaults to github — the only forge today — so the section can grow to
// another code host without a config break.
func TestLoadInstances_defaultsKindAndURL(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "forges:\n  - name: prs\n    token: ghp_forge\n")
	clearForgeEnv(t, "PRS")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "github", instances[0].Kind)
	assert.Equal(t, "https://api.github.com", instances[0].URL)
}

// The token may come from the environment, per instance, exactly as a tracker's
// does — FORGE_<NAME>_TOKEN.
func TestLoadInstances_tokenFromEnv(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "forges:\n  - name: prs\n")
	clearForgeEnv(t, "PRS")
	t.Setenv("FORGE_PRS_TOKEN", "ghp_from_env")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.NotNil(t, instances[0].Forge)
}

// No forges: section is not an error — it is a config that cannot open pull
// requests, which the caller reports where it matters.
func TestLoadInstances_noSection(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "githubs:\n  - name: work\n    token: ghp_abc\n")
	clearForgeEnv(t, "WORK")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}
