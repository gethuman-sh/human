package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/config"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(body), 0o600))
	return dir
}

func TestContainerModel_valid(t *testing.T) {
	dir := writeConfig(t, "agent:\n  model: Sonnet\n")

	assert.Equal(t, "sonnet", ContainerModel(dir), "the alias vocabulary is case-insensitive")

	_, ok := AgentModelProblem(dir)
	assert.False(t, ok, "a recognised value is not a problem")
}

// A typo must cost nothing but the setting: passing it through would break every
// container launch for the project, and unchanged behaviour is the documented
// failure mode when the setting is absent (SC-5474).
func TestContainerModel_unknownValueFallsBack(t *testing.T) {
	dir := writeConfig(t, "agent:\n  model: sonett\n")

	assert.Empty(t, ContainerModel(dir))

	p, ok := AgentModelProblem(dir)
	require.True(t, ok)
	assert.Equal(t, "unknown-agent-model", p.Rule)
	assert.Equal(t, config.Warning, p.Severity)
	assert.Equal(t, "agent", p.Section)
	assert.Contains(t, p.Message, "sonett")
	assert.Contains(t, p.Fix, "opus")
}

func TestContainerModel_unsetHasNoProblem(t *testing.T) {
	dir := t.TempDir()

	assert.Empty(t, ContainerModel(dir))
	_, ok := AgentModelProblem(dir)
	assert.False(t, ok)

	withFile := writeConfig(t, "project: infra\n")
	assert.Empty(t, ContainerModel(withFile))
	_, ok = AgentModelProblem(withFile)
	assert.False(t, ok)
}
