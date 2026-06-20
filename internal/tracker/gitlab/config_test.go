package gitlab

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
			yaml: "gitlabs:\n  - name: work\n    url: https://gitlab.com\n    token: glpat-abc\n",
			want: []Config{
				{Name: "work", URL: "https://gitlab.com", Token: "glpat-abc"},
			},
		},
		{
			name: "multiple entries",
			yaml: "gitlabs:\n  - name: work\n    url: https://gitlab.com\n    token: glpat-abc\n  - name: self-hosted\n    url: https://gitlab.example.com\n    token: glpat-xyz\n",
			want: []Config{
				{Name: "work", URL: "https://gitlab.com", Token: "glpat-abc"},
				{Name: "self-hosted", URL: "https://gitlab.example.com", Token: "glpat-xyz"},
			},
		},
		{
			name: "empty list",
			yaml: "gitlabs: []\n",
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
	writeConfig(t, dir, "gitlabs:\n  - name: work\n    url: https://gitlab.com\n    token: glpat-abc\n")

	unsetEnv(t, "GITLAB_URL")
	unsetEnv(t, "GITLAB_TOKEN")
	unsetEnv(t, "GITLAB_WORK_URL")
	unsetEnv(t, "GITLAB_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)

	assert.Equal(t, "work", instances[0].Name)
	assert.Equal(t, "gitlab", instances[0].Kind)
	assert.Equal(t, "https://gitlab.com", instances[0].URL)
	assert.NotNil(t, instances[0].Provider)
}

func TestLoadInstances_defaultURL(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "gitlabs:\n  - name: work\n    token: glpat-abc\n")

	unsetEnv(t, "GITLAB_URL")
	unsetEnv(t, "GITLAB_TOKEN")
	unsetEnv(t, "GITLAB_WORK_URL")
	unsetEnv(t, "GITLAB_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "https://gitlab.com", instances[0].URL)
}

func TestLoadInstances_incompleteConfigSkipped(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "gitlabs:\n  - name: work\n    url: https://gitlab.com\n")

	unsetEnv(t, "GITLAB_URL")
	unsetEnv(t, "GITLAB_TOKEN")
	unsetEnv(t, "GITLAB_WORK_URL")
	unsetEnv(t, "GITLAB_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}
