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

// Fetch updates the origin remote-tracking refs for branch in the repository at
// dir (running `git -C <dir> fetch origin <branch>`), so a freshness check reads
// the current remote tip rather than a stale local mirror. It is a package
// variable so callers can stub git access in tests without a real repository.
var Fetch = func(ctx context.Context, dir, branch string) error {
	if _, err := runner(ctx, "git", "-C", dir, "fetch", "origin", branch); err != nil {
		return errors.WrapWithDetails(err, "fetching branch from origin", "dir", dir, "branch", branch)
	}
	return nil
}

// IsAncestor reports whether ancestor is an ancestor of descendant in the
// repository at dir (running `git -C <dir> merge-base --is-ancestor <ancestor>
// <descendant>`). git exits 0 when it is, 1 when it is not; any other exit is a
// real error and also yields false. Used to decide whether a branch already
// contains the current base tip (i.e. is mergeable without a rebase). Package
// var so callers can stub git access in tests.
var IsAncestor = func(ctx context.Context, dir, ancestor, descendant string) bool {
	_, err := runner(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

// RevParse resolves rev to its full commit SHA in the repository at dir
// (running `git -C <dir> rev-parse <rev>`). It is used to record the remote tip
// a lease push must match. Package var so callers can stub git access in tests.
var RevParse = func(ctx context.Context, dir, rev string) (string, error) {
	out, err := runner(ctx, "git", "-C", dir, "rev-parse", rev)
	if err != nil {
		return "", errors.WrapWithDetails(err, "resolving revision", "dir", dir, "rev", rev)
	}
	return strings.TrimSpace(string(out)), nil
}

// Rebase replays branch onto base in the repository at dir (running
// `git -C <dir> rebase <base> <branch>`). On failure it aborts the in-progress
// rebase so the worktree is never left mid-rebase for the next pipeline step;
// the returned error signals a real conflict the mechanical path cannot resolve.
// Package var so callers can stub git access in tests.
var Rebase = func(ctx context.Context, dir, base, branch string) error {
	if _, err := runner(ctx, "git", "-C", dir, "rebase", base, branch); err != nil {
		// Leaving a half-applied rebase would strand the worktree; abort so a
		// retry starts from a clean state. The abort's own error is irrelevant —
		// the conflict is the failure we report.
		_, _ = runner(ctx, "git", "-C", dir, "rebase", "--abort")
		return errors.WrapWithDetails(err, "rebasing branch onto base", "dir", dir, "base", base, "branch", branch)
	}
	return nil
}

// PushWithLease force-pushes branch to origin only if the remote tip still
// matches expectedRemoteSHA (running `git -C <dir> push --force-with-lease=
// <branch>:<sha> origin <branch>`). The lease is what lets a rebased branch
// advance a diverged remote tip WITHOUT clobbering a concurrent push: if the
// remote moved off expectedRemoteSHA the push is refused. Package var so callers
// can stub git access in tests.
var PushWithLease = func(ctx context.Context, dir, branch, expectedRemoteSHA string) error {
	lease := "--force-with-lease=" + branch + ":" + expectedRemoteSHA
	if _, err := runner(ctx, "git", "-C", dir, "push", lease, "origin", branch); err != nil {
		return errors.WrapWithDetails(err, "lease-pushing branch to origin", "dir", dir, "branch", branch, "expected", expectedRemoteSHA)
	}
	return nil
}

// CommitReachable reports whether sha is reachable from branch in the repository
// at dir. It resolves branch as a local ref when present (a board-context fix
// leaves its branch local on the machine that produced it), else as
// origin/<branch>, then runs `git -C <dir> merge-base --is-ancestor <sha> <ref>`.
// An empty branch or sha short-circuits to false. Package var so callers can
// stub git access in tests.
var CommitReachable = func(ctx context.Context, dir, branch, sha string) bool {
	if branch == "" || sha == "" {
		return false
	}
	ref := branch
	if !BranchExistsLocal(ctx, dir, branch) {
		ref = "origin/" + branch
	}
	_, err := runner(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", sha, ref)
	return err == nil
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
