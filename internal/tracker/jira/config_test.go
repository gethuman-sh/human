package jira

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
			yaml: "jiras:\n  - name: work\n    url: https://work.atlassian.net\n    user: me@work.com\n    key: tok1\n",
			want: []Config{
				{Name: "work", URL: "https://work.atlassian.net", User: "me@work.com", Key: "tok1"},
			},
		},
		{
			name: "multiple entries",
			yaml: "jiras:\n  - name: work\n    url: https://work.atlassian.net\n    user: me@work.com\n    key: tok1\n  - name: personal\n    url: https://personal.atlassian.net\n    user: me@personal.com\n    key: tok2\n",
			want: []Config{
				{Name: "work", URL: "https://work.atlassian.net", User: "me@work.com", Key: "tok1"},
				{Name: "personal", URL: "https://personal.atlassian.net", User: "me@personal.com", Key: "tok2"},
			},
		},
		{
			name: "empty list",
			yaml: "jiras: []\n",
			want: []Config{},
		},
		{
			name:    "invalid YAML",
			yaml:    ":\n  :\n  invalid: [unterminated",
			wantErr: "parsing config file",
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

func TestLoadConfigs_extensionlessFallback(t *testing.T) {
	dir := t.TempDir()
	content := "jiras:\n  - name: work\n    url: https://work.atlassian.net\n    user: me@work.com\n    key: tok1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig"), []byte(content), 0o644))

	got, err := LoadConfigs(dir)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "work", got[0].Name)
}

func TestLoadInstances_happyPath(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "jiras:\n  - name: work\n    url: https://work.atlassian.net\n    user: me@work.com\n    key: tok1\n")

	unsetEnv(t, "JIRA_URL")
	unsetEnv(t, "JIRA_USER")
	unsetEnv(t, "JIRA_KEY")
	unsetEnv(t, "JIRA_WORK_URL")
	unsetEnv(t, "JIRA_WORK_USER")
	unsetEnv(t, "JIRA_WORK_KEY")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)

	assert.Equal(t, "work", instances[0].Name)
	assert.Equal(t, "jira", instances[0].Kind)
	assert.Equal(t, "https://work.atlassian.net", instances[0].URL)
	assert.Equal(t, "me@work.com", instances[0].User)
	assert.NotNil(t, instances[0].Provider)
}

func TestLoadInstances_multipleEntries(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "jiras:\n  - name: work\n    url: https://work.atlassian.net\n    user: me@work.com\n    key: tok1\n  - name: personal\n    url: https://personal.atlassian.net\n    user: me@personal.com\n    key: tok2\n")

	unsetEnv(t, "JIRA_URL")
	unsetEnv(t, "JIRA_USER")
	unsetEnv(t, "JIRA_KEY")
	unsetEnv(t, "JIRA_WORK_URL")
	unsetEnv(t, "JIRA_WORK_USER")
	unsetEnv(t, "JIRA_WORK_KEY")
	unsetEnv(t, "JIRA_PERSONAL_URL")
	unsetEnv(t, "JIRA_PERSONAL_USER")
	unsetEnv(t, "JIRA_PERSONAL_KEY")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Len(t, instances, 2)
	assert.Equal(t, "work", instances[0].Name)
	assert.Equal(t, "personal", instances[1].Name)
}

func TestLoadInstances_missingFile(t *testing.T) {
	dir := t.TempDir()

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestLoadInstances_instanceEnvOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	// Config file has no key — the instance must get its credential from env.
	writeConfig(t, dir, "jiras:\n  - name: work\n    url: https://work.atlassian.net\n    user: me@work.com\n")

	unsetEnv(t, "JIRA_URL")
	unsetEnv(t, "JIRA_USER")
	unsetEnv(t, "JIRA_WORK_URL")
	unsetEnv(t, "JIRA_WORK_USER")
	// Set only instance-specific key, no global — instance should still be created.
	unsetEnv(t, "JIRA_KEY")
	t.Setenv("JIRA_WORK_KEY", "instance-key")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1, "instance-specific env should provide the credential")
	assert.Equal(t, "https://work.atlassian.net", instances[0].URL)
	assert.NotNil(t, instances[0].Provider)
}

func TestLoadInstances_incompleteConfigSkipped(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "jiras:\n  - name: work\n    url: https://work.atlassian.net\n")

	unsetEnv(t, "JIRA_URL")
	unsetEnv(t, "JIRA_USER")
	unsetEnv(t, "JIRA_KEY")
	unsetEnv(t, "JIRA_WORK_URL")
	unsetEnv(t, "JIRA_WORK_USER")
	unsetEnv(t, "JIRA_WORK_KEY")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}
