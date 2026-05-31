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
