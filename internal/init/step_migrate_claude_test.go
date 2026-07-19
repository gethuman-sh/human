package init

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/home/user/src/foo", "-home-user-src-foo"},
		{"/workspaces/cli", "-workspaces-cli"},
		{"/", "-"},
		{"/home/stephan/Development/human/cli", "-home-stephan-Development-human-cli"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, EncodePath(tt.input))
		})
	}
}

func TestBuildReplacements_SortedLongestFirst(t *testing.T) {
	repls := BuildReplacements(
		"/home/stephan/Development/human/cli",
		"/workspaces/cli",
		"/home/stephan/.claude",
		"/home/vscode/.claude",
	)

	// Verify longest-first ordering.
	for i := 1; i < len(repls); i++ {
		assert.GreaterOrEqual(t, len(repls[i-1].Old), len(repls[i].Old),
			"replacements should be sorted longest-first")
	}

	// Verify the project path and encoded path are both present.
	var hasProj, hasEncoded, hasClaude bool
	for _, r := range repls {
		if r.Old == "/home/stephan/Development/human/cli" {
			assert.Equal(t, "/workspaces/cli", r.New)
			hasProj = true
		}
		if r.Old == "-home-stephan-Development-human-cli" {
			assert.Equal(t, "-workspaces-cli", r.New)
			hasEncoded = true
		}
		if r.Old == "/home/stephan/.claude" {
			assert.Equal(t, "/home/vscode/.claude", r.New)
			hasClaude = true
		}
	}
	assert.True(t, hasProj, "project path replacement missing")
	assert.True(t, hasEncoded, "encoded path replacement missing")
	assert.True(t, hasClaude, "claude dir replacement missing")
}

func TestBuildReplacements_SameClaudeDir(t *testing.T) {
	repls := BuildReplacements(
		"/home/user/proj",
		"/workspaces/proj",
		"/home/user/.claude",
		"/home/user/.claude", // same — should not add claude replacement
	)

	for _, r := range repls {
		assert.NotEqual(t, "/home/user/.claude", r.Old,
			"should not add claude dir replacement when old == new")
	}
}

func TestApplyReplacements(t *testing.T) {
	repls := []replacement{
		{Old: "/home/stephan/Development/human/cli", New: "/workspaces/cli"},
		{Old: "-home-stephan-Development-human-cli", New: "-workspaces-cli"},
	}

	input := `{"cwd": "/home/stephan/Development/human/cli", "sessionId": "abc123"}`
	want := `{"cwd": "/workspaces/cli", "sessionId": "abc123"}`
	assert.Equal(t, want, applyReplacements(input, repls))
}

func TestApplyReplacements_LongestFirstPreventsPartialMatch(t *testing.T) {
	repls := BuildReplacements(
		"/home/user/long/path/project",
		"/workspaces/project",
		"/home/user/.claude",
		"/home/user/.claude",
	)

	input := "/home/user/long/path/project/subdir"
	result := applyReplacements(input, repls)
	assert.Equal(t, "/workspaces/project/subdir", result)
}

func TestIsTextFile(t *testing.T) {
	tmp := t.TempDir()

	// Known text extension.
	jsonFile := filepath.Join(tmp, "test.jsonl")
	require.NoError(t, os.WriteFile(jsonFile, []byte(`{"key":"value"}`), 0o644))
	assert.True(t, isTextFile(jsonFile))

	// Binary file with null bytes.
	binFile := filepath.Join(tmp, "test.bin")
	require.NoError(t, os.WriteFile(binFile, []byte{0x00, 0x01, 0x02}, 0o644))
	assert.False(t, isTextFile(binFile))

	// Unknown extension but text content.
	txtFile := filepath.Join(tmp, "test.unknown")
	require.NoError(t, os.WriteFile(txtFile, []byte("hello world"), 0o644))
	assert.True(t, isTextFile(txtFile))
}

func TestDiscoverSessionIDs(t *testing.T) {
	tmp := t.TempDir()

	// Create session files.
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "abc-123.jsonl"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "def-456.jsonl"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "sessions-index.jsonl"), []byte("{}"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "abc-123"), 0o755))

	ids := DiscoverSessionIDs(tmp)
	assert.ElementsMatch(t, []string{"abc-123", "def-456"}, ids)
}

func TestDiscoverSessionIDs_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	ids := DiscoverSessionIDs(tmp)
	assert.Empty(t, ids)
}

func TestDiscoverSessionIDs_NoDir(t *testing.T) {
	ids := DiscoverSessionIDs("/nonexistent/path")
	assert.Nil(t, ids)
}

func TestCopyDirWithRewrite(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")

	// Create source structure.
	require.NoError(t, os.MkdirAll(filepath.Join(src, "subdir"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(src, "session.jsonl"),
		[]byte(`{"cwd": "/old/path"}`+"\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(src, "subdir", "agent.jsonl"),
		[]byte(`{"cwd": "/old/path/sub"}`+"\n"),
		0o644,
	))

	repls := []replacement{{Old: "/old/path", New: "/new/path"}}
	count, err := copyDirWithRewrite(src, dst, repls)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Verify rewriting.
	data, err := os.ReadFile(filepath.Join(dst, "session.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "/new/path")
	assert.NotContains(t, string(data), "/old/path")

	data, err = os.ReadFile(filepath.Join(dst, "subdir", "agent.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "/new/path/sub")
}

func TestClaudeMigrateStep_NoClaudeData(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)

	step := &claudeMigrateStep{
		prompter:  &mockMigratePrompter{confirmMigrate: true, containerPath: "/workspaces/test"},
		claudeDir: filepath.Join(tmp, ".claude"),
	}

	var buf bytes.Buffer
	hints, err := step.Run(&buf, newMockFileWriter())
	require.NoError(t, err)
	assert.Nil(t, hints)
	assert.Contains(t, buf.String(), "No Claude Code sessions found")
}

func TestClaudeMigrateStep_UserDeclines(t *testing.T) {
	tmp := t.TempDir()

	// Create a project dir so migration is offered.
	projDir := filepath.Join(tmp, "myproject")
	require.NoError(t, os.MkdirAll(projDir, 0o755))
	t.Chdir(projDir)

	claudeDir := filepath.Join(tmp, ".claude")
	key := EncodePath(projDir)
	projectMeta := filepath.Join(claudeDir, "projects", key)
	require.NoError(t, os.MkdirAll(projectMeta, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectMeta, "session1.jsonl"),
		[]byte(`{"cwd": "`+projDir+`"}`+"\n"),
		0o644,
	))

	step := &claudeMigrateStep{
		prompter:  &mockMigratePrompter{confirmMigrate: false},
		claudeDir: claudeDir,
	}

	var buf bytes.Buffer
	hints, err := step.Run(&buf, newMockFileWriter())
	require.NoError(t, err)
	assert.Nil(t, hints)
	assert.NotContains(t, buf.String(), "Migrated")
}

func TestClaudeMigrateStep_FullMigration(t *testing.T) {
	tmp := t.TempDir()

	// Set up host project.
	projDir := filepath.Join(tmp, "myproject")
	require.NoError(t, os.MkdirAll(projDir, 0o755))
	// Resolve symlinks so EncodePath matches os.Getwd() on macOS (/var → /private/var).
	if real, err := filepath.EvalSymlinks(projDir); err == nil {
		projDir = real
	}
	t.Chdir(projDir)

	claudeDir := filepath.Join(tmp, ".claude")
	oldKey := EncodePath(projDir)
	projectMeta := filepath.Join(claudeDir, "projects", oldKey)
	require.NoError(t, os.MkdirAll(projectMeta, 0o755))

	// Create session JSONL.
	sessionData := `{"cwd": "` + projDir + `", "sessionId": "sess-001"}` + "\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(projectMeta, "sess-001.jsonl"),
		[]byte(sessionData),
		0o644,
	))

	// Create session subdir with subagent.
	require.NoError(t, os.MkdirAll(filepath.Join(projectMeta, "sess-001", "subagents"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectMeta, "sess-001", "subagents", "agent1.jsonl"),
		[]byte(`{"cwd": "`+projDir+`"}`+"\n"),
		0o644,
	))

	// Create memory file.
	require.NoError(t, os.MkdirAll(filepath.Join(projectMeta, "memory"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectMeta, "memory", "note.md"),
		[]byte("Project at "+projDir+"\n"),
		0o644,
	))

	// Create history.jsonl.
	historyEntry := `{"project": "` + projDir + `", "sessionId": "sess-001", "display": "test"}` + "\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(claudeDir, "history.jsonl"),
		[]byte(historyEntry),
		0o644,
	))

	containerPath := "/workspaces/myproject"
	step := &claudeMigrateStep{
		prompter:  &mockMigratePrompter{confirmMigrate: true, containerPath: containerPath},
		claudeDir: claudeDir,
	}

	var buf bytes.Buffer
	hints, err := step.Run(&buf, newMockFileWriter())
	require.NoError(t, err)
	assert.Nil(t, hints)
	assert.Contains(t, buf.String(), "Migrated")
	assert.Contains(t, buf.String(), containerPath)

	// Verify new project dir exists with rewritten content.
	newKey := EncodePath(containerPath)
	newProjectDir := filepath.Join(claudeDir, "projects", newKey)
	assert.DirExists(t, newProjectDir)

	data, err := os.ReadFile(filepath.Join(newProjectDir, "sess-001.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(data), containerPath)
	assert.NotContains(t, string(data), projDir)

	// Verify subagent rewritten.
	data, err = os.ReadFile(filepath.Join(newProjectDir, "sess-001", "subagents", "agent1.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(data), containerPath)

	// Verify memory rewritten.
	data, err = os.ReadFile(filepath.Join(newProjectDir, "memory", "note.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), containerPath)

	// Verify history.jsonl has new entry appended.
	historyData, err := os.ReadFile(filepath.Join(claudeDir, "history.jsonl"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(historyData)), "\n")
	assert.GreaterOrEqual(t, len(lines), 2, "should have original + migrated entry")
	assert.Contains(t, lines[len(lines)-1], containerPath)
}

// mockMigratePrompter is a test double for ClaudeMigratePrompter.
// TestRewriteDirInPlace_PreservesBinaries verifies that rewriteDirInPlace
// only touches text files; binary files in the same directory are returned
// untouched, byte-for-byte. This guards against the regression where
// migrating onto the source path would truncate every binary file in
// session-env directories.
func TestRewriteDirInPlace_PreservesBinaries(t *testing.T) {
	dir := t.TempDir()

	// Text file with content the rewriter should change.
	textPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(textPath, []byte(`{"path":"/old/proj"}`), 0o600))

	// "Binary" file with embedded null bytes — isTextFile should reject it.
	binPath := filepath.Join(dir, "blob.bin")
	binContent := []byte{0x00, 0x01, 0x02, 'o', 'l', 'd', 0x00, 0xff}
	require.NoError(t, os.WriteFile(binPath, binContent, 0o600))

	repls := []replacement{{Old: "/old/proj", New: "/new/proj"}}

	require.NoError(t, rewriteDirInPlace(dir, repls))

	// Text file rewritten.
	gotText, err := os.ReadFile(textPath)
	require.NoError(t, err)
	assert.Equal(t, `{"path":"/new/proj"}`, string(gotText))

	// Binary file untouched, byte-for-byte.
	gotBin, err := os.ReadFile(binPath)
	require.NoError(t, err)
	assert.Equal(t, binContent, gotBin)
}

type mockMigratePrompter struct {
	confirmMigrate bool
	containerPath  string
}

func (m *mockMigratePrompter) ConfirmMigrateClaude() (bool, error) {
	return m.confirmMigrate, nil
}

func (m *mockMigratePrompter) PromptContainerPath(detected string) (string, error) {
	if m.containerPath != "" {
		return m.containerPath, nil
	}
	return detected, nil
}
