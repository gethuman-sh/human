package cmdutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestAutoSaveTrackerConfig_newFile(t *testing.T) {
	dir := t.TempDir()
	parsed := &tracker.ParsedURL{
		Kind:    "jira",
		BaseURL: "https://myco.atlassian.net",
		Key:     "HUM-4",
	}

	err := AutoSaveTrackerConfig(parsed, dir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml"))
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "jiras:")
	assert.Contains(t, content, "name: myco")
	assert.Contains(t, content, "url: https://myco.atlassian.net")
}

func TestAutoSaveTrackerConfig_existingFileNewSection(t *testing.T) {
	dir := t.TempDir()
	existing := "githubs:\n  - name: personal\n    url: https://api.github.com\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(existing), 0o644))

	parsed := &tracker.ParsedURL{
		Kind:    "jira",
		BaseURL: "https://myco.atlassian.net",
		Key:     "HUM-4",
	}

	err := AutoSaveTrackerConfig(parsed, dir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml"))
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "githubs:")
	assert.Contains(t, content, "jiras:")
	assert.Contains(t, content, "name: myco")
}

func TestAutoSaveTrackerConfig_existingSectionAppend(t *testing.T) {
	dir := t.TempDir()
	existing := "jiras:\n  - name: old\n    url: https://old.atlassian.net\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(existing), 0o644))

	parsed := &tracker.ParsedURL{
		Kind:    "jira",
		BaseURL: "https://newco.atlassian.net",
		Key:     "HUM-4",
	}

	err := AutoSaveTrackerConfig(parsed, dir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml"))
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "name: old")
	assert.Contains(t, content, "name: newco")
	assert.Contains(t, content, "url: https://newco.atlassian.net")
}

func TestAutoSaveTrackerConfig_alreadyConfigured(t *testing.T) {
	dir := t.TempDir()
	existing := "jiras:\n  - name: myco\n    url: https://myco.atlassian.net\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(existing), 0o644))

	parsed := &tracker.ParsedURL{
		Kind:    "jira",
		BaseURL: "https://myco.atlassian.net",
		Key:     "HUM-4",
	}

	err := AutoSaveTrackerConfig(parsed, dir)
	require.NoError(t, err)

	// File should not be modified.
	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml"))
	require.NoError(t, err)
	assert.Equal(t, existing, string(data))
}

func TestAutoSaveTrackerConfig_azuredevops(t *testing.T) {
	dir := t.TempDir()
	parsed := &tracker.ParsedURL{
		Kind:    "azuredevops",
		BaseURL: "https://dev.azure.com",
		Key:     "myproject/42",
		Org:     "myorg",
	}

	err := AutoSaveTrackerConfig(parsed, dir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml"))
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "azuredevops:")
	assert.Contains(t, content, "name: myorg")
	assert.Contains(t, content, "org: myorg")
}

func TestInstanceNameFromURL(t *testing.T) {
	tests := []struct {
		name     string
		parsed   *tracker.ParsedURL
		expected string
	}{
		{
			name:     "atlassian cloud",
			parsed:   &tracker.ParsedURL{BaseURL: "https://amazingcto.atlassian.net"},
			expected: "amazingcto",
		},
		{
			name:     "github",
			parsed:   &tracker.ParsedURL{BaseURL: "https://api.github.com"},
			expected: "github",
		},
		{
			name:     "gitlab",
			parsed:   &tracker.ParsedURL{BaseURL: "https://gitlab.com"},
			expected: "gitlab",
		},
		{
			name:     "azure with org",
			parsed:   &tracker.ParsedURL{BaseURL: "https://dev.azure.com", Org: "myorg"},
			expected: "myorg",
		},
		{
			name:     "shortcut",
			parsed:   &tracker.ParsedURL{BaseURL: "https://api.app.shortcut.com"},
			expected: "shortcut",
		},
		{
			name:     "self-hosted jira",
			parsed:   &tracker.ParsedURL{BaseURL: "https://jira.mycompany.com"},
			expected: "jira",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, instanceNameFromURL(tt.parsed))
		})
	}
}
