package cmdconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCfg(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o600))
}

func TestRunMigrate_reportsWhatItWrote(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "githubs:\n  - name: human\n    token: gh://token\n")

	var buf bytes.Buffer
	require.NoError(t, RunMigrate(&buf, dir, false))

	out := buf.String()
	assert.Contains(t, out, `Wrote forges: entry "human"`)
	assert.Contains(t, out, "Updated:")
}

// A dry run says "would", not "did". Reporting a write that did not happen is
// how someone concludes their config is fixed when it is not.
func TestRunMigrate_dryRunSaysWould(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "githubs:\n  - name: human\n    token: gh://token\n")

	var buf bytes.Buffer
	require.NoError(t, RunMigrate(&buf, dir, true))

	out := buf.String()
	assert.Contains(t, out, "Would write")
	assert.NotContains(t, out, "Updated:")
}

func TestRunMigrate_nothingToDo(t *testing.T) {
	dir := t.TempDir()
	writeCfg(t, dir, "shortcuts:\n  - name: board\n    token: x\n")

	var buf bytes.Buffer
	require.NoError(t, RunMigrate(&buf, dir, false))
	assert.Contains(t, buf.String(), "Nothing to migrate")
}

func TestRunMigrate_noConfig(t *testing.T) {
	var buf bytes.Buffer
	require.Error(t, RunMigrate(&buf, t.TempDir(), false))
}

func TestBuildConfigCmd_hasMigrate(t *testing.T) {
	cmd := BuildConfigCmd()
	assert.Equal(t, "config", cmd.Use)

	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Use == "migrate" {
			found = true
		}
	}
	assert.True(t, found)
}
