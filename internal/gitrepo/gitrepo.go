// Package gitrepo reads facts about the local git repository by shelling out
// to git. It is the single place that runs git from Go (elsewhere git is only
// invoked from agent prompts), so the exec is isolated and testable.
package gitrepo

import (
	"context"
	"os/exec"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// runner executes a command and returns its combined stdout. It is a package
// variable so tests can stub git invocation without a real repository.
var runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output() // #nosec G204 -- only called with the hardcoded "git" command and fixed subcommands
}

// OriginURL returns the URL of the "origin" remote for the repository at dir
// (running `git -C <dir> remote get-url origin`). dir may be "." for the
// current working directory. The returned value is trimmed of surrounding
// whitespace.
//
// It is a package variable so callers in other packages can stub git access
// in their own tests without a real repository.
var OriginURL = func(ctx context.Context, dir string) (string, error) {
	out, err := runner(ctx, "git", "-C", dir, "remote", "get-url", "origin")
	if err != nil {
		return "", errors.WrapWithDetails(err, "reading git origin remote", "dir", dir)
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return "", errors.WithDetails("git origin remote is empty", "dir", dir)
	}
	return url, nil
}

// Push pushes branch to the origin remote of the repository at dir (running
// `git -C <dir> push origin <branch>`). It is a package variable so callers can
// stub git access in tests without a real repository.
var Push = func(ctx context.Context, dir, branch string) error {
	if _, err := runner(ctx, "git", "-C", dir, "push", "origin", branch); err != nil {
		return errors.WrapWithDetails(err, "pushing branch to origin", "dir", dir, "branch", branch)
	}
	return nil
}

// DefaultBranch returns the repository's default branch by resolving
// origin/HEAD (running `git -C <dir> symbolic-ref refs/remotes/origin/HEAD`)
// and stripping the leading "origin/". It falls back to "main" when origin/HEAD
// is not set locally. It is a package variable so tests can stub git access.
var DefaultBranch = func(ctx context.Context, dir string) string {
	out, err := runner(ctx, "git", "-C", dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return "main"
	}
	ref := strings.TrimSpace(string(out))
	ref = strings.TrimPrefix(ref, "refs/remotes/")
	if branch := strings.TrimPrefix(ref, "origin/"); branch != "" {
		return branch
	}
	return "main"
}

// IsRepo reports whether dir is inside a git working tree (running
// `git -C <dir> rev-parse --is-inside-work-tree`). Package var so callers can
// stub git access in tests.
var IsRepo = func(ctx context.Context, dir string) bool {
	out, err := runner(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// BranchExistsLocal reports whether branch exists as a local ref in the
// repository at dir (running `git -C <dir> rev-parse --verify --quiet
// refs/heads/<branch>`). An empty branch is never a ref, so it short-circuits to
// false without invoking git. Package var so callers can stub git access in tests.
var BranchExistsLocal = func(ctx context.Context, dir, branch string) bool {
	if branch == "" {
		return false
	}
	_, err := runner(ctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// BranchExistsRemote reports whether branch exists on the origin remote of the
// repository at dir (running `git -C <dir> ls-remote --heads origin <branch>`).
// ls-remote exits zero with empty output when the branch is absent, so a ref is
// present only when the output is non-empty. An empty branch short-circuits to
// false. Package var so callers can stub git access in tests.
var BranchExistsRemote = func(ctx context.Context, dir, branch string) bool {
	if branch == "" {
		return false
	}
	out, err := runner(ctx, "git", "-C", dir, "ls-remote", "--heads", "origin", branch)
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// BranchReachable reports whether branch resolves on THIS machine for the
// repository at dir — as a local ref or on origin. The local check comes first so
// a branch left local (a board-context fix on the machine that produced it) is
// reachable without a network round-trip; once the branch is pushed, the remote
// check also passes. Package var so callers can stub git access in tests.
var BranchReachable = func(ctx context.Context, dir, branch string) bool {
	return BranchExistsLocal(ctx, dir, branch) || BranchExistsRemote(ctx, dir, branch)
}

// WorktreeAdd creates a detached private worktree at worktreePath rooted at base
// (running `git -C <repoDir> worktree add --detach <worktreePath> <base>`). The
// worktree shares repoDir's object DB, so branches created inside it are visible
// from repoDir. Package var so callers can stub git access.
var WorktreeAdd = func(ctx context.Context, repoDir, worktreePath, base string) error {
	if _, err := runner(ctx, "git", "-C", repoDir, "worktree", "add", "--detach", worktreePath, base); err != nil {
		return errors.WrapWithDetails(err, "adding git worktree", "repo", repoDir, "worktree", worktreePath, "base", base)
	}
	return nil
}

// WorktreeRemove force-removes the worktree at worktreePath (running
// `git -C <repoDir> worktree remove --force <worktreePath>`). Force because a
// completed run may leave modified/untracked files. Package var for test stubs.
var WorktreeRemove = func(ctx context.Context, repoDir, worktreePath string) error {
	if _, err := runner(ctx, "git", "-C", repoDir, "worktree", "remove", "--force", worktreePath); err != nil {
		return errors.WrapWithDetails(err, "removing git worktree", "repo", repoDir, "worktree", worktreePath)
	}
	return nil
}
