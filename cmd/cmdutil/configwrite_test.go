package cmdutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/config"
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

	// A file created today uses the shape a config written today uses.
	assert.Contains(t, content, "trackers:")
	assert.Contains(t, content, "kind: jira")
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

	assert.Contains(t, content, "kind: azuredevops")
	assert.Contains(t, content, "name: myorg")
	assert.Contains(t, content, "org: myorg")
}

// An existing file keeps its shape: adding a tracker must not restructure
// someone's config on the way past.
func TestAutoSaveTrackerConfig_keepsAnExistingFilesShape(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"),
		[]byte("githubs:\n  - name: personal\n    url: https://api.github.com\n"), 0o600))

	require.NoError(t, AutoSaveTrackerConfig(&tracker.ParsedURL{
		Kind: "jira", BaseURL: "https://myco.atlassian.net",
	}, dir))

	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "jiras:")
	assert.NotContains(t, string(data), "trackers:")
}

// The hazard the string surgery could not see: a section name inside a comment
// was a valid splice point, so the entry landed inside somebody's prose.
func TestAutoSaveTrackerConfig_isNotFooledByAComment(t *testing.T) {
	dir := t.TempDir()
	original := "# jiras: are configured elsewhere, see the wiki\nshortcuts:\n  - name: board\n    token: t\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(original), 0o600))

	require.NoError(t, AutoSaveTrackerConfig(&tracker.ParsedURL{
		Kind: "jira", BaseURL: "https://myco.atlassian.net",
	}, dir))

	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml"))
	require.NoError(t, err)
	out := string(data)

	assert.Contains(t, out, "# jiras: are configured elsewhere, see the wiki",
		"the comment is prose and stays prose")
	// The entry lands in a real section, and the config still parses into the
	// two trackers it now describes.
	doc, err := config.Load(dir)
	require.NoError(t, err)
	require.Len(t, doc.Trackers(), 2)
}

// Comments elsewhere in the file survive an append, which raw text splicing
// only managed by accident.
func TestAutoSaveTrackerConfig_keepsComments(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"),
		[]byte("# my project\njiras:\n  # the old one\n  - name: old\n    url: https://old.atlassian.net\n"), 0o600))

	require.NoError(t, AutoSaveTrackerConfig(&tracker.ParsedURL{
		Kind: "jira", BaseURL: "https://newco.atlassian.net",
	}, dir))

	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "# my project")
	assert.Contains(t, string(data), "# the old one")
	assert.Contains(t, string(data), "name: newco")
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
