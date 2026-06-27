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
