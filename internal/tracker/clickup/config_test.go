package clickup

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
			yaml: "clickups:\n  - name: work\n    url: https://api.clickup.com/api\n    token: pk_12345\n    team_id: \"9876\"\n",
			want: []Config{
				{Name: "work", URL: "https://api.clickup.com/api", Token: "pk_12345", TeamID: "9876"},
			},
		},
		{
			name: "multiple entries",
			yaml: "clickups:\n  - name: work\n    url: https://api.clickup.com/api\n    token: pk_12345\n  - name: personal\n    url: https://api.clickup.com/api\n    token: pk_67890\n",
			want: []Config{
				{Name: "work", URL: "https://api.clickup.com/api", Token: "pk_12345"},
				{Name: "personal", URL: "https://api.clickup.com/api", Token: "pk_67890"},
			},
		},
		{
			name: "empty list",
			yaml: "clickups: []\n",
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
	writeConfig(t, dir, "clickups:\n  - name: work\n    url: https://api.clickup.com/api\n    token: pk_12345\n")

	unsetEnv(t, "CLICKUP_URL")
	unsetEnv(t, "CLICKUP_TOKEN")
	unsetEnv(t, "CLICKUP_WORK_URL")
	unsetEnv(t, "CLICKUP_WORK_TOKEN")
	unsetEnv(t, "CLICKUP_TEAM_ID")
	unsetEnv(t, "CLICKUP_WORK_TEAM_ID")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)

	assert.Equal(t, "work", instances[0].Name)
	assert.Equal(t, "clickup", instances[0].Kind)
	assert.Equal(t, "https://api.clickup.com/api", instances[0].URL)
	assert.NotNil(t, instances[0].Provider)
}

func TestLoadInstances_multipleEntries(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "clickups:\n  - name: work\n    url: https://api.clickup.com/api\n    token: pk_12345\n  - name: personal\n    token: pk_67890\n")

	unsetEnv(t, "CLICKUP_URL")
	unsetEnv(t, "CLICKUP_TOKEN")
	unsetEnv(t, "CLICKUP_TEAM_ID")
	unsetEnv(t, "CLICKUP_WORK_URL")
	unsetEnv(t, "CLICKUP_WORK_TOKEN")
	unsetEnv(t, "CLICKUP_WORK_TEAM_ID")
	unsetEnv(t, "CLICKUP_PERSONAL_URL")
	unsetEnv(t, "CLICKUP_PERSONAL_TOKEN")
	unsetEnv(t, "CLICKUP_PERSONAL_TEAM_ID")

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

func TestLoadInstances_defaultURL(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "clickups:\n  - name: work\n    token: pk_12345\n")

	unsetEnv(t, "CLICKUP_URL")
	unsetEnv(t, "CLICKUP_TOKEN")
	unsetEnv(t, "CLICKUP_TEAM_ID")
	unsetEnv(t, "CLICKUP_WORK_URL")
	unsetEnv(t, "CLICKUP_WORK_TOKEN")
	unsetEnv(t, "CLICKUP_WORK_TEAM_ID")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "https://api.clickup.com", instances[0].URL)
}

func TestLoadInstances_incompleteConfigSkipped(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "clickups:\n  - name: work\n    url: https://api.clickup.com/api\n")

	unsetEnv(t, "CLICKUP_URL")
	unsetEnv(t, "CLICKUP_TOKEN")
	unsetEnv(t, "CLICKUP_TEAM_ID")
	unsetEnv(t, "CLICKUP_WORK_URL")
	unsetEnv(t, "CLICKUP_WORK_TOKEN")
	unsetEnv(t, "CLICKUP_WORK_TEAM_ID")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestLoadInstances_instanceEnvOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	// Config file has no token -- the instance must get its credential from env.
	writeConfig(t, dir, "clickups:\n  - name: work\n    url: https://api.clickup.com/api\n")

	unsetEnv(t, "CLICKUP_URL")
	unsetEnv(t, "CLICKUP_WORK_URL")
	unsetEnv(t, "CLICKUP_TEAM_ID")
	unsetEnv(t, "CLICKUP_WORK_TEAM_ID")
	// Set only instance-specific token, no global -- instance should still be created.
	unsetEnv(t, "CLICKUP_TOKEN")
	t.Setenv("CLICKUP_WORK_TOKEN", "instance-token")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1, "instance-specific env should provide the credential")
	assert.Equal(t, "https://api.clickup.com/api", instances[0].URL)
	assert.NotNil(t, instances[0].Provider)
}
