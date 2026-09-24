package cmddaemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every daemon launch path builds its StartOpts here and passes the registered
// project's directory as configDir, so reading the project's own agent.model in
// this one factory is what makes the setting reach all of them (SC-5474).
func TestDockerAgentLauncher_startOptsCarriesTheProjectModel(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte("agent:\n  model: sonnet\n"), 0o600))

	opts := dockerAgentLauncher{}.startOpts("board-sc-1-x", "p", dir, dir, "")

	assert.Equal(t, "sonnet", opts.Model)
}

// Absent the setting the container must start exactly as it does today:
// BuildClaudeArgs omits --model entirely for an empty value.
func TestDockerAgentLauncher_startOptsLeavesModelEmptyWithoutSetting(t *testing.T) {
	dir := t.TempDir()

	opts := dockerAgentLauncher{}.startOpts("board-sc-1-x", "p", dir, dir, "")

	assert.Empty(t, opts.Model)
}
