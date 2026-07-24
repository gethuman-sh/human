package cmdmockups

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/mockups"
)

// writeWinner records a winner and, when file is non-empty, creates its HTML on
// disk under mockups/<slug>/<file>.
func writeWinner(t *testing.T, dir, key, slug, file string) {
	t.Helper()
	store := mockups.NewStore(mockups.PathIn(dir))
	require.NoError(t, store.Set(key, mockups.Entry{Slug: mockups.SlugFor(key)}))
	require.NoError(t, store.Choose(key, mockups.Choice{Slug: slug, File: file}))
	if file != "" {
		groupDir := filepath.Join(dir, "mockups", slug)
		require.NoError(t, os.MkdirAll(groupDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(groupDir, file), []byte("<html></html>"), 0o600))
	}
}

func TestMockupsChosen_PrintsPath(t *testing.T) {
	dir := t.TempDir()
	writeWinner(t, dir, "SC-1", "sc-1-o3-v1", "02.html")

	var buf bytes.Buffer
	require.NoError(t, RunMockupsChosen(&buf, dir, "SC-1"))

	want := filepath.Join(dir, "mockups", "sc-1-o3-v1", "02.html")
	assert.Equal(t, want, strings.TrimSpace(buf.String()))
}

func TestMockupsChosen_FileMissing(t *testing.T) {
	dir := t.TempDir()
	// Winner recorded but the HTML never written (e.g. pruned since).
	store := mockups.NewStore(mockups.PathIn(dir))
	require.NoError(t, store.Choose("SC-1", mockups.Choice{Slug: "sc-1-o3-v1", File: "02.html"}))

	var buf bytes.Buffer
	require.NoError(t, RunMockupsChosen(&buf, dir, "SC-1"))
	assert.Empty(t, buf.String())
}

func TestMockupsChosen_NoWinner(t *testing.T) {
	dir := t.TempDir()
	store := mockups.NewStore(mockups.PathIn(dir))
	require.NoError(t, store.Set("SC-1", mockups.Entry{Slug: "sc-1"}))

	var buf bytes.Buffer
	require.NoError(t, RunMockupsChosen(&buf, dir, "SC-1"))
	assert.Empty(t, buf.String())
}

func TestMockupsChosen_NoStore(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	require.NoError(t, RunMockupsChosen(&buf, dir, "SC-1"))
	assert.Empty(t, buf.String())
}

func TestBuildMockupsCmd_HasChosen(t *testing.T) {
	cmd := BuildMockupsCmd()
	assert.Equal(t, "mockups", cmd.Name())
	sub, _, err := cmd.Find([]string{"chosen"})
	require.NoError(t, err)
	assert.Equal(t, "chosen", sub.Name())
}

// chdir switches the working directory to dir for the duration of the test, so
// the `chosen` command's os.Getwd resolves to a controlled project.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func TestMockupsCmd_ChosenPrintsPathViaExecute(t *testing.T) {
	dir := t.TempDir()
	writeWinner(t, dir, "SC-1", "sc-1-o3-v1", "02.html")
	chdir(t, dir)

	cmd := BuildMockupsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"chosen", "SC-1"})
	require.NoError(t, cmd.Execute())

	// os.Getwd may resolve symlinks (e.g. /tmp → /private/tmp on macOS); assert
	// on the tail the command appends rather than the absolute prefix.
	assert.Contains(t, buf.String(), filepath.Join("mockups", "sc-1-o3-v1", "02.html"))
}
