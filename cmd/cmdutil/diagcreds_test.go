package cmdutil

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestWarnSkippedTrackers_missingCreds(t *testing.T) {
	dir := t.TempDir()
	cfg := "jiras:\n  - name: work\n    url: https://work.atlassian.net\n    user: alice@work.com\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(cfg), 0o644))

	// No instances loaded (creds missing).
	var buf bytes.Buffer
	skipped := WarnSkippedTrackers(&buf, dir, nil)

	assert.True(t, skipped)
	assert.Contains(t, buf.String(), "Skipped jira/work")
	assert.Contains(t, buf.String(), "JIRA_KEY")
	// user is in the config file, so it should NOT be reported as missing.
	assert.NotContains(t, buf.String(), "JIRA_USER")
}

func TestWarnSkippedTrackers_allLoaded(t *testing.T) {
	dir := t.TempDir()
	cfg := "githubs:\n  - name: personal\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(cfg), 0o644))

	loaded := []tracker.Instance{
		{Name: "personal", Kind: "github"},
	}

	var buf bytes.Buffer
	skipped := WarnSkippedTrackers(&buf, dir, loaded)

	assert.False(t, skipped)
	assert.Empty(t, buf.String())
}

func TestWarnSkippedTrackers_noConfig(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	skipped := WarnSkippedTrackers(&buf, dir, nil)

	assert.False(t, skipped)
	assert.Empty(t, buf.String())
}

func TestWarnSkippedTrackers_multipleSkipped(t *testing.T) {
	dir := t.TempDir()
	cfg := "linears:\n  - name: team\ngitlabs:\n  - name: work\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(cfg), 0o644))

	var buf bytes.Buffer
	skipped := WarnSkippedTrackers(&buf, dir, nil)

	assert.True(t, skipped)
	assert.Contains(t, buf.String(), "LINEAR_TOKEN")
	assert.Contains(t, buf.String(), "GITLAB_TOKEN")
}
