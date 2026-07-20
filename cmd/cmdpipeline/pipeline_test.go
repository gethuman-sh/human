package cmdpipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// run executes the pipeline command tree with args in a temp working dir.
func run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })

	cmd := BuildPipelineCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return buf.String(), err
}

func TestPipeline_initPrintsPaths(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, dir, "init", "bugs")
	require.NoError(t, err)
	assert.Contains(t, out, filepath.Join(".human", "bugs"))
	assert.Contains(t, out, ".bugs-candidates.md")
}

func TestPipeline_appendCountReportCleanup(t *testing.T) {
	dir := t.TempDir()

	out, err := run(t, dir, "append", "bugs", "--file", "a.go", "--line", "10", "--category", "logic", "--title", "off by one")
	require.NoError(t, err)
	assert.Contains(t, out, `"id": "C-001"`)
	assert.Contains(t, out, `"duplicate": false`)

	out, err = run(t, dir, "append", "bugs", "--file", "a.go", "--line", "10", "--category", "logic", "--title", "again")
	require.NoError(t, err)
	assert.Contains(t, out, `"duplicate": true`)

	out, err = run(t, dir, "count", "bugs")
	require.NoError(t, err)
	assert.Equal(t, "1", strings.TrimSpace(out))

	out, err = run(t, dir, "report", "bugs")
	require.NoError(t, err)
	assert.Contains(t, out, filepath.Join(".human", "bugs", "bugs-"))

	_, err = run(t, dir, "cleanup", "bugs")
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Join(dir, ".human", "bugs"))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestPipeline_appendBodyFromStdinLikeFile(t *testing.T) {
	dir := t.TempDir()
	bodyPath := filepath.Join(dir, "body.md")
	require.NoError(t, os.WriteFile(bodyPath, []byte("evidence here\n"), 0o600))

	_, err := run(t, dir, "append", "bugs", "--file", "a.go", "--line", "1", "--category", "logic", "--title", "t", "--body-file", bodyPath)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, ".human", "bugs", ".bugs-candidates.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "evidence here")
}

func TestPipeline_stateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	out, err := run(t, dir, "state", "get", "bugs", "iterations")
	require.NoError(t, err)
	assert.Equal(t, "", strings.TrimSpace(out))

	_, err = run(t, dir, "state", "set", "bugs", "iterations", "3")
	require.NoError(t, err)

	out, err = run(t, dir, "state", "get", "bugs", "iterations")
	require.NoError(t, err)
	assert.Equal(t, "3", strings.TrimSpace(out))
}

func TestPipeline_missingRequiredFlags(t *testing.T) {
	dir := t.TempDir()
	_, err := run(t, dir, "append", "bugs", "--line", "1")
	require.Error(t, err)
}

func TestPipeline_badName(t *testing.T) {
	dir := t.TempDir()
	_, err := run(t, dir, "init", "../escape")
	require.Error(t, err)
}
