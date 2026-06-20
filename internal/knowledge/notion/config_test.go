package notion

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
			yaml: "notions:\n  - name: work\n    url: https://api.notion.com\n    token: ntn_abc\n    description: Company workspace\n",
			want: []Config{
				{Name: "work", URL: "https://api.notion.com", Token: "ntn_abc", Description: "Company workspace"},
			},
		},
		{
			name: "multiple entries",
			yaml: "notions:\n  - name: work\n    token: ntn_abc\n  - name: personal\n    token: ntn_xyz\n",
			want: []Config{
				{Name: "work", Token: "ntn_abc"},
				{Name: "personal", Token: "ntn_xyz"},
			},
		},
		{
			name: "empty list",
			yaml: "notions: []\n",
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
	writeTestConfig(t, dir, "notions:\n  - name: work\n    url: https://api.notion.com\n    token: ntn_abc\n")

	unsetEnv(t, "NOTION_URL")
	unsetEnv(t, "NOTION_TOKEN")
	unsetEnv(t, "NOTION_WORK_URL")
	unsetEnv(t, "NOTION_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)

	assert.Equal(t, "work", instances[0].Name)
	assert.Equal(t, "https://api.notion.com", instances[0].URL)
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
	writeTestConfig(t, dir, "notions:\n  - name: work\n    token: ntn_abc\n")

	unsetEnv(t, "NOTION_URL")
	unsetEnv(t, "NOTION_TOKEN")
	unsetEnv(t, "NOTION_WORK_URL")
	unsetEnv(t, "NOTION_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "https://api.notion.com", instances[0].URL)
}

func TestLoadInstances_missingTokenSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "notions:\n  - name: work\n    url: https://api.notion.com\n")

	unsetEnv(t, "NOTION_URL")
	unsetEnv(t, "NOTION_TOKEN")
	unsetEnv(t, "NOTION_WORK_URL")
	unsetEnv(t, "NOTION_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestLoadInstances_instanceEnvOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	// Config file has no token — the instance must get its credential from env.
	writeTestConfig(t, dir, "notions:\n  - name: work\n    url: https://api.notion.com\n")

	unsetEnv(t, "NOTION_URL")
	unsetEnv(t, "NOTION_WORK_URL")
	// Set only instance-specific token, no global — instance should still be created.
	unsetEnv(t, "NOTION_TOKEN")
	t.Setenv("NOTION_WORK_TOKEN", "instance-token")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1, "instance-specific env should provide the credential")
	assert.Equal(t, "https://api.notion.com", instances[0].URL)
	assert.NotNil(t, instances[0].Client)
}
