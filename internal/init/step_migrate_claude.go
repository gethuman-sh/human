package init

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude"
)

// ClaudeMigratePrompter abstracts TUI interactions for the Claude migration step.
type ClaudeMigratePrompter interface {
	ConfirmMigrateClaude() (bool, error)
	PromptContainerPath(detected string) (string, error)
}

type claudeMigrateStep struct {
	prompter ClaudeMigratePrompter
	// claudeDir overrides ~/.claude for testing. Empty uses the real path.
	claudeDir string
}

// NewClaudeMigrateStep creates a WizardStep that migrates Claude Code session
// data for use inside a devcontainer with a different project path.
func NewClaudeMigrateStep(p ClaudeMigratePrompter) WizardStep {
	return &claudeMigrateStep{prompter: p}
}

func (s *claudeMigrateStep) Name() string { return "migrate-claude" }

func (s *claudeMigrateStep) Run(w io.Writer, _ claude.FileWriter) ([]string, error) {
	claudeDir := s.resolveClaudeDir()

	// Detect current project path.
	hostPath, err := os.Getwd()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "detecting working directory")
	}

	// Check if Claude project data exists.
	oldKey := EncodePath(hostPath)
	oldProjectDir := filepath.Join(claudeDir, "projects", oldKey)
	if _, statErr := os.Stat(oldProjectDir); os.IsNotExist(statErr) {
		_, _ = fmt.Fprintln(w, "No Claude Code sessions found for this project, skipping migration.")
		return nil, nil
	}

	// Ask user if they want to migrate.
	migrate, err := s.prompter.ConfirmMigrateClaude()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "confirming Claude migration")
	}
	if !migrate {
		return nil, nil
	}

	// Ask for the container project path.
	defaultContainer := "/workspaces/" + filepath.Base(hostPath)
	containerPath, err := s.prompter.PromptContainerPath(defaultContainer)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "prompting container path")
	}
	if containerPath == "" {
		containerPath = defaultContainer
	}

	newKey := EncodePath(containerPath)
	newProjectDir := filepath.Join(claudeDir, "projects", newKey)

	// Build replacement map.
	replacements := BuildReplacements(hostPath, containerPath, claudeDir, claudeDir)

	// Discover session IDs.
	sessions := DiscoverSessionIDs(oldProjectDir)

	// Copy project directory with path rewriting.
	fileCount, err := copyDirWithRewrite(oldProjectDir, newProjectDir, replacements)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "copying project sessions",
			"from", oldProjectDir, "to", newProjectDir)
	}

	// Copy session-related directories (file-history, session-env, todos).
	migrateSessionDirs(claudeDir, sessions, replacements)

	// Append to history.jsonl. Surface the failure to the user before
	// printing the success message so the two lines don't contradict
	// each other when the history file could not be updated.
	historyErr := appendHistory(claudeDir, oldKey, newKey, containerPath, replacements)
	if historyErr != nil {
		errors.LogError(historyErr).Msg("failed to update history.jsonl")
		_, _ = fmt.Fprintf(w, "\nWarning: could not update Claude history: %v\n", historyErr)
	}

	_, _ = fmt.Fprintf(w, "\nMigrated %d files across %d sessions.\n", fileCount, len(sessions))
	if historyErr == nil {
		_, _ = fmt.Fprintf(w, "Claude --continue will work at %s\n", containerPath)
	} else {
		_, _ = fmt.Fprintf(w, "Claude --continue may not reach %s until the history file is repaired\n", containerPath)
	}

	return nil, nil
}

// migrateSessionDirs rewrites text files under each session-env directory in
// place and rewrites todo files for each session. Binary files are left
// untouched so they are never truncated by an in-place "copy" onto themselves.
func migrateSessionDirs(claudeDir string, sessions []string, repls []replacement) {
	for _, sid := range sessions {
		// session-env/{session-id}/
		src := filepath.Join(claudeDir, "session-env", sid)
		if _, statErr := os.Stat(src); statErr == nil {
			if rewriteErr := rewriteDirInPlace(src, repls); rewriteErr != nil {
				errors.LogError(rewriteErr).Str("session", sid).Msg("failed to rewrite session-env")
			}
		}

		// todos/{session-id}-*.json
		rewriteSessionTodos(claudeDir, sid, repls)
	}
}

// rewriteDirInPlace walks dir and rewrites every text file found, applying
// the given replacements. Binary files are skipped entirely so they are
// never truncated by an in-place copy onto themselves.
func rewriteDirInPlace(dir string, repls []replacement) error {
	if len(repls) == 0 {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !isTextFile(path) {
			return nil
		}
		return rewriteFile(path, path, repls)
	})
}

// rewriteSessionTodos rewrites todo files matching a session ID.
func rewriteSessionTodos(claudeDir, sid string, repls []replacement) {
	todosDir := filepath.Join(claudeDir, "todos")
	entries, readErr := os.ReadDir(todosDir)
	if readErr != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), sid+"-") {
			srcFile := filepath.Join(todosDir, entry.Name())
			if err := rewriteFile(srcFile, srcFile, repls); err != nil {
				errors.LogError(err).Str("file", entry.Name()).Msg("failed to rewrite todo")
			}
		}
	}
}

func (s *claudeMigrateStep) resolveClaudeDir() string {
	if s.claudeDir != "" {
		return s.claudeDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".claude")
	}
	return filepath.Join(home, ".claude")
}

// EncodePath converts an absolute path to the Claude project key format.
// /home/user/src/foo → -home-user-src-foo
func EncodePath(path string) string {
	return "-" + strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", "-")
}

// replacement is a single old→new string substitution.
type replacement struct {
	Old string
	New string
}

// BuildReplacements creates the replacement pairs sorted longest-first
// to prevent substring collisions during rewriting.
func BuildReplacements(oldProj, newProj, oldClaude, newClaude string) []replacement {
	pairs := []replacement{
		{Old: oldProj, New: newProj},
		{Old: EncodePath(oldProj), New: EncodePath(newProj)},
	}

	if oldClaude != newClaude {
		pairs = append(pairs, replacement{Old: oldClaude, New: newClaude})
	}

	// Add symlink-resolved variants.
	realProj, err := filepath.EvalSymlinks(oldProj)
	if err == nil && realProj != oldProj {
		pairs = append(pairs,
			replacement{Old: realProj, New: newProj},
			replacement{Old: EncodePath(realProj), New: EncodePath(newProj)},
		)
	}

	realClaude, err := filepath.EvalSymlinks(oldClaude)
	if err == nil && realClaude != oldClaude && oldClaude != newClaude {
		pairs = append(pairs, replacement{Old: realClaude, New: newClaude})
	}

	// Sort longest-first to prevent partial matches.
	sort.Slice(pairs, func(i, j int) bool {
		return len(pairs[i].Old) > len(pairs[j].Old)
	})

	return pairs
}

// DiscoverSessionIDs reads JSONL filenames from a project directory to find session IDs.
func DiscoverSessionIDs(projectDir string) []string {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil
	}

	var ids []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".jsonl") && name != "sessions-index.jsonl" {
			ids = append(ids, strings.TrimSuffix(name, ".jsonl"))
		}
	}
	return ids
}

// isTextFile determines if a file should be rewritten (text) or copied as-is (binary).
func isTextFile(path string) bool {
	textExts := map[string]bool{
		".json": true, ".jsonl": true, ".txt": true, ".md": true,
		".yaml": true, ".yml": true, ".toml": true, ".cfg": true,
		".ini": true, ".log": true, ".csv": true,
	}

	ext := strings.ToLower(filepath.Ext(path))
	if textExts[ext] {
		return true
	}

	// Heuristic: read first 8KB and check for null bytes.
	f, err := os.Open(path) // #nosec G304 — path from local ~/.claude/ filesystem walk
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 8192)
	n, _ := f.Read(buf)
	for i := range n {
		if buf[i] == 0 {
			return false
		}
	}
	return n > 0
}

// applyReplacements applies all replacement pairs to a string.
func applyReplacements(s string, repls []replacement) string {
	for _, r := range repls {
		s = strings.ReplaceAll(s, r.Old, r.New)
	}
	return s
}

// rewriteFile reads a text file, applies replacements, and writes to dst.
// src and dst may be the same path (in-place rewrite).
func rewriteFile(src, dst string, repls []replacement) error {
	if len(repls) == 0 {
		if src != dst {
			return copyFileBinary(src, dst)
		}
		return nil
	}

	data, err := os.ReadFile(src) // #nosec G304 — path from local ~/.claude/ filesystem walk
	if err != nil {
		return errors.WrapWithDetails(err, "reading file", "path", src)
	}

	rewritten := applyReplacements(string(data), repls)

	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return errors.WrapWithDetails(err, "creating directory", "path", filepath.Dir(dst))
	}

	return os.WriteFile(dst, []byte(rewritten), 0o600)
}

// copyFileBinary copies a file without modification. The destination file is
// closed explicitly so a flush failure (quota, I/O error) surfaces as an
// error rather than being silently dropped by a deferred Close.
func copyFileBinary(src, dst string) (retErr error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}

	in, err := os.Open(src) // #nosec G304 — path from local ~/.claude/ filesystem walk
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst) // #nosec G304 — path from local ~/.claude/ filesystem walk
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && retErr == nil {
			retErr = errors.WrapWithDetails(cerr, "closing destination", "path", dst)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// copyDirWithRewrite recursively copies a directory, rewriting text files.
// Returns the number of files copied.
func copyDirWithRewrite(src, dst string, repls []replacement) (int, error) {
	count := 0
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}

		count++
		if isTextFile(path) && len(repls) > 0 {
			return rewriteFile(path, target, repls)
		}
		return copyFileBinary(path, target)
	})
	return count, err
}

// appendHistory adds migrated session entries to ~/.claude/history.jsonl.
func appendHistory(claudeDir, oldKey, _, _ string, repls []replacement) error {
	historyPath := filepath.Join(claudeDir, "history.jsonl")

	// Read existing history to find entries for the old project.
	srcFile, err := os.Open(historyPath) // #nosec G304 — path built from ~/.claude/history.jsonl
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No history file, nothing to do.
		}
		return errors.WrapWithDetails(err, "opening history.jsonl", "path", historyPath)
	}
	defer func() { _ = srcFile.Close() }()

	// Match lines containing any of the old paths from the replacement map.
	var newEntries []string
	scanner := bufio.NewScanner(srcFile)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for long lines
	for scanner.Scan() {
		line := scanner.Text()
		matched := false
		for _, r := range repls {
			if strings.Contains(line, r.Old) {
				matched = true
				break
			}
		}
		if matched {
			rewritten := applyReplacements(line, repls)
			newEntries = append(newEntries, rewritten)
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.WrapWithDetails(err, "reading history.jsonl")
	}

	if len(newEntries) == 0 {
		return nil
	}

	// Append new entries. Explicitly capture the close error so a flush
	// failure surfaces instead of being lost in a deferred close.
	f, err := os.OpenFile(historyPath, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 — path built from ~/.claude/history.jsonl
	if err != nil {
		return errors.WrapWithDetails(err, "opening history.jsonl for append")
	}
	var writeErr error
	for _, entry := range newEntries {
		if _, werr := fmt.Fprintln(f, entry); werr != nil { // #nosec G705 — entry from local history.jsonl, not user input
			writeErr = errors.WrapWithDetails(werr, "writing history entry")
			break
		}
	}
	if cerr := f.Close(); cerr != nil && writeErr == nil {
		writeErr = errors.WrapWithDetails(cerr, "closing history.jsonl")
	}
	return writeErr
}
