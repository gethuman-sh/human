package agent

import (
	"context"
	"os"
	"path/filepath"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/gitrepo"
)

// WorktreesDir returns ~/.human/worktrees, sibling to the agent-logs and agents
// dirs, so the same retention sweep tree owns kept worktrees. Falls back to
// ./.human/worktrees when the home dir is unknown.
func WorktreesDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "worktrees")
	}
	return filepath.Join(home, ".human", "worktrees")
}

// isolateWorkspace resolves the per-run bind-mount source and returns the
// worktree path (empty when the workspace is not a git repo and was mounted
// directly) plus a cleanup that removes a freshly created worktree. The cleanup
// is for a launch that fails before becoming a tracked agent; a live agent's
// worktree is torn down through stopLocked instead.
func (m *Manager) isolateWorkspace(ctx context.Context, projectDir, name string) (workspace, worktree string, cleanup func(), err error) {
	mountSource, err := m.mountSourceForRun(ctx, projectDir, name)
	if err != nil {
		return "", "", func() {}, errors.WrapWithDetails(err, "isolating agent workspace", "name", name)
	}
	if mountSource != projectDir {
		worktree = mountSource
	}
	cleanup = func() {
		if worktree != "" {
			_ = gitrepo.WorktreeRemove(ctx, projectDir, worktree)
		}
	}
	return mountSource, worktree, cleanup, nil
}

// mountSourceForRun resolves the per-run bind-mount source: a private detached
// git worktree of the shared checkout so concurrent agents never share
// HEAD/index/tree. Non-git workspaces have no corruption hazard and mount
// directly.
func (m *Manager) mountSourceForRun(ctx context.Context, projectDir, agentName string) (string, error) {
	if !gitrepo.IsRepo(ctx, projectDir) {
		return projectDir, nil
	}
	// A short random suffix keeps re-runs of a deterministic board agent name
	// from colliding with a prior run's KEPT (forensic) worktree.
	worktreePath := filepath.Join(WorktreesDir(), agentName+"-"+newExecID()[:12])
	if err := os.MkdirAll(WorktreesDir(), 0o700); err != nil {
		return "", errors.WrapWithDetails(err, "creating worktrees dir", "dir", WorktreesDir())
	}
	base := gitrepo.DefaultBranch(ctx, projectDir)
	if err := gitrepo.WorktreeAdd(ctx, projectDir, worktreePath, base); err != nil {
		return "", err
	}
	if err := provisionAgentAssets(projectDir, worktreePath); err != nil {
		// A half-provisioned workspace would launch an agent that dies later
		// with a misleading "exited without completing the stage" — fail the
		// launch loudly instead, and don't leave the fresh worktree behind.
		_ = gitrepo.WorktreeRemove(ctx, projectDir, worktreePath)
		return "", errors.WrapWithDetails(err, "provisioning agent worktree", "worktree", worktreePath)
	}
	return worktreePath, nil
}

// agentAssetDirs and agentAssetFiles are the project-level Claude assets an
// agent needs inside its isolated worktree. .claude/ is git-ignored, so a
// fresh worktree carries none of it — without the skills the agent's slash
// command (/human-autofix, /human-plan, …) is unknown and the run dies at
// startup (ticket 478). Allow-listed rather than copied wholesale so session
// state (.claude/worktrees, .claude/codenav, …) never leaks into a run.
var agentAssetDirs = []string{"skills", "commands", "agents"}
var agentAssetFiles = []string{"settings.json", "settings.local.json"}

// provisionAgentAssets copies the project's .claude agent assets into the
// worktree. A project without .claude (or without an individual asset) is
// fine — agents then run bare, exactly as they would in the shared checkout.
func provisionAgentAssets(projectDir, worktreePath string) error {
	srcRoot := filepath.Join(projectDir, ".claude")
	if _, err := os.Stat(srcRoot); err != nil {
		return nil
	}
	dstRoot := filepath.Join(worktreePath, ".claude")
	for _, dir := range agentAssetDirs {
		src := filepath.Join(srcRoot, dir)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyTree(src, filepath.Join(dstRoot, dir)); err != nil {
			return err
		}
	}
	for _, file := range agentAssetFiles {
		src := filepath.Join(srcRoot, file)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, filepath.Join(dstRoot, file)); err != nil {
			return err
		}
	}
	return nil
}

// copyTree copies a directory recursively: regular files with their mode
// preserved (skills may carry executable helpers), directories created as
// needed. Irregular entries (sockets, device nodes) are skipped; symlinks are
// followed via os.Open so a linked skill file still arrives as content.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return errors.WrapWithDetails(err, "walking agent assets", "path", path)
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return errors.WrapWithDetails(relErr, "resolving asset path", "path", path)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if !d.Type().IsRegular() && d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src) // #nosec G304 -- paths derive from the project's own .claude dir
	if err != nil {
		return errors.WrapWithDetails(err, "reading agent asset", "src", src)
	}
	info, err := os.Stat(src)
	if err != nil {
		return errors.WrapWithDetails(err, "stat agent asset", "src", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return errors.WrapWithDetails(err, "creating asset dir", "dir", filepath.Dir(dst))
	}
	if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
		return errors.WrapWithDetails(err, "writing agent asset", "dst", dst)
	}
	return nil
}
