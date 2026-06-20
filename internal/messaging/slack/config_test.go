package slack

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
			yaml: "slacks:\n  - name: work\n    token: \"xoxb-123\"\n    channel: C0123456789\n    description: Team notifications\n",
			want: []Config{
				{Name: "work", Token: "xoxb-123", Channel: "C0123456789", Description: "Team notifications"},
			},
		},
		{
			name: "multiple entries",
			yaml: "slacks:\n  - name: team1\n    token: tok1\n    channel: C111\n  - name: team2\n    token: tok2\n    channel: C222\n",
			want: []Config{
				{Name: "team1", Token: "tok1", Channel: "C111"},
				{Name: "team2", Token: "tok2", Channel: "C222"},
			},
		},
		{
			name: "empty list",
			yaml: "slacks: []\n",
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
	writeTestConfig(t, dir, "slacks:\n  - name: work\n    token: \"xoxb-123\"\n    channel: C0123456789\n")

	unsetEnv(t, "SLACK_TOKEN")
	unsetEnv(t, "SLACK_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)

	assert.Equal(t, "work", instances[0].Name)
	assert.Equal(t, "C0123456789", instances[0].Channel)
	assert.NotNil(t, instances[0].Client)
}

func TestLoadInstances_missingFile(t *testing.T) {
	dir := t.TempDir()
	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestLoadInstances_missingTokenSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "slacks:\n  - name: work\n    channel: C0123456789\n")

	unsetEnv(t, "SLACK_TOKEN")
	unsetEnv(t, "SLACK_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestLoadInstances_missingChannelSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "slacks:\n  - name: work\n    token: \"xoxb-123\"\n")

	unsetEnv(t, "SLACK_TOKEN")
	unsetEnv(t, "SLACK_WORK_TOKEN")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances)
}
