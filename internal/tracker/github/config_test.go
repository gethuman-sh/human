package github

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unsetEnv registers cleanup via t.Setenv then unsets the variable for the test.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	require.NoError(t, os.Unsetenv(key))
}

// writeConfig writes a .humanconfig.yaml file in dir with the given content.
func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644))
}

func TestLoadConfigs(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    []Config
		wantErr string
	}{
		{
			name: "single entry",
			yaml: "githubs:\n  - name: personal\n    url: https://api.github.com\n    token: ghp_abc\n",
			want: []Config{
				{Name: "personal", URL: "https://api.github.com", Token: "ghp_abc"},
			},
		},
		{
			name: "multiple entries",
			yaml: "githubs:\n  - name: personal\n    url: https://api.github.com\n    token: ghp_abc\n  - name: work\n    url: https://github.example.com/api/v3\n    token: ghp_xyz\n",
			want: []Config{
				{Name: "personal", URL: "https://api.github.com", Token: "ghp_abc"},
				{Name: "work", URL: "https://github.example.com/api/v3", Token: "ghp_xyz"},
			},
		},
		{
			name: "empty list",
			yaml: "githubs: []\n",
			want: []Config{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tt.yaml)

			got, err := LoadConfigs(dir)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

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
	writeConfig(t, dir, "githubs:\n  - name: personal\n    url: https://api.github.com\n    token: ghp_abc\n")

	unsetEnv(t, "GITHUB_URL")
	unsetEnv(t, "GITHUB_TOKEN")
	unsetEnv(t, "GITHUB_PERSONAL_URL")
	unsetEnv(t, "GITHUB_PERSONAL_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)

	assert.Equal(t, "personal", instances[0].Name)
	assert.Equal(t, "github", instances[0].Kind)
	assert.Equal(t, "https://api.github.com", instances[0].URL)
	assert.NotNil(t, instances[0].Provider)
}

func TestLoadInstances_multipleEntries(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "githubs:\n  - name: personal\n    url: https://api.github.com\n    token: ghp_abc\n  - name: work\n    url: https://ghe.example.com/api/v3\n    token: ghp_xyz\n")

	unsetEnv(t, "GITHUB_URL")
	unsetEnv(t, "GITHUB_TOKEN")
	unsetEnv(t, "GITHUB_PERSONAL_URL")
	unsetEnv(t, "GITHUB_PERSONAL_TOKEN")
	unsetEnv(t, "GITHUB_WORK_URL")
	unsetEnv(t, "GITHUB_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Len(t, instances, 2)
	assert.Equal(t, "personal", instances[0].Name)
	assert.Equal(t, "work", instances[1].Name)
}

func TestLoadInstances_missingFile(t *testing.T) {
	dir := t.TempDir()

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestLoadInstances_defaultURL(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "githubs:\n  - name: personal\n    token: ghp_abc\n")

	unsetEnv(t, "GITHUB_URL")
	unsetEnv(t, "GITHUB_TOKEN")
	unsetEnv(t, "GITHUB_PERSONAL_URL")
	unsetEnv(t, "GITHUB_PERSONAL_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "https://api.github.com", instances[0].URL)
}

func TestLoadInstances_instanceEnvOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	// Config file has no token — the instance must get its credential from env.
	writeConfig(t, dir, "githubs:\n  - name: work\n    url: https://api.github.com\n")

	unsetEnv(t, "GITHUB_URL")
	unsetEnv(t, "GITHUB_WORK_URL")
	// Set only instance-specific token, no global — instance should still be created.
	unsetEnv(t, "GITHUB_TOKEN")
	t.Setenv("GITHUB_WORK_TOKEN", "instance-token")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1, "instance-specific env should provide the credential")
	assert.Equal(t, "https://api.github.com", instances[0].URL)
	assert.NotNil(t, instances[0].Provider)
}

func TestLoadInstances_incompleteConfigSkipped(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "githubs:\n  - name: work\n    url: https://api.github.com\n")

	unsetEnv(t, "GITHUB_URL")
	unsetEnv(t, "GITHUB_TOKEN")
	unsetEnv(t, "GITHUB_WORK_URL")
	unsetEnv(t, "GITHUB_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}
