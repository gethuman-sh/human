package figma

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

func writeTestConfig(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644))
}

func TestLoadConfigs(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []Config
	}{
		{
			name: "single entry",
			yaml: "figmas:\n  - name: design\n    url: https://api.figma.com\n    token: figd_abc\n    description: Product design team\n",
			want: []Config{
				{Name: "design", URL: "https://api.figma.com", Token: "figd_abc", Description: "Product design team"},
			},
		},
		{
			name: "multiple entries",
			yaml: "figmas:\n  - name: design\n    token: figd_abc\n  - name: marketing\n    token: figd_xyz\n",
			want: []Config{
				{Name: "design", Token: "figd_abc"},
				{Name: "marketing", Token: "figd_xyz"},
			},
		},
		{
			name: "empty list",
			yaml: "figmas: []\n",
			want: []Config{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTestConfig(t, dir, tt.yaml)

			got, err := LoadConfigs(dir)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLoadConfigs_missingFile(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadConfigs(dir)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestLoadInstances_happyPath(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "figmas:\n  - name: design\n    url: https://api.figma.com\n    token: figd_abc\n")

	unsetEnv(t, "FIGMA_URL")
	unsetEnv(t, "FIGMA_TOKEN")
	unsetEnv(t, "FIGMA_DESIGN_URL")
	unsetEnv(t, "FIGMA_DESIGN_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)

	assert.Equal(t, "design", instances[0].Name)
	assert.Equal(t, "https://api.figma.com", instances[0].URL)
	assert.NotNil(t, instances[0].Client)
}

func TestLoadInstances_missingFile(t *testing.T) {
	dir := t.TempDir()
	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestLoadInstances_defaultURL(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "figmas:\n  - name: design\n    token: figd_abc\n")

	unsetEnv(t, "FIGMA_URL")
	unsetEnv(t, "FIGMA_TOKEN")
	unsetEnv(t, "FIGMA_DESIGN_URL")
	unsetEnv(t, "FIGMA_DESIGN_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "https://api.figma.com", instances[0].URL)
}

func TestLoadInstances_missingTokenSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "figmas:\n  - name: design\n    url: https://api.figma.com\n")

	unsetEnv(t, "FIGMA_URL")
	unsetEnv(t, "FIGMA_TOKEN")
	unsetEnv(t, "FIGMA_DESIGN_URL")
	unsetEnv(t, "FIGMA_DESIGN_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestLoadInstances_globalEnvOverride(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "figmas:\n  - name: design\n    url: https://api.figma.com\n    token: file-token\n")

	unsetEnv(t, "FIGMA_URL")
	t.Setenv("FIGMA_TOKEN", "global-token")
	unsetEnv(t, "FIGMA_DESIGN_URL")
	unsetEnv(t, "FIGMA_DESIGN_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "https://api.figma.com", instances[0].URL)
}
