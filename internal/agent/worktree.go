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
	return worktreePath, nil
}
