package vault

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// ghRefPrefix is the scheme for GitHub CLI secret references.
const ghRefPrefix = "gh://"

// ghRefPattern is the accepted reference grammar: "gh://token" for the
// default host, "gh://<hostname>/token" for a specific one (GitHub
// Enterprise). The hostname whitelist excludes shell metacharacters and must
// not start with '-' so no config value can smuggle a rogue flag to gh.
var ghRefPattern = regexp.MustCompile(`^gh://(?:([A-Za-z0-9][A-Za-z0-9.\-]*)/)?token$`)

// GhCLI resolves gh:// secret references by shelling out to the GitHub CLI.
// The gh keyring stays the single source of the token — nothing is copied
// into config files or environment variables.
type GhCLI struct {
	// Binary is the gh CLI binary name. Defaults to "gh".
	Binary string

	// runner overrides command execution for testing.
	runner func(ctx context.Context, binary string, args ...string) ([]byte, error)
}

// NewGhCLI creates a GitHub CLI secret provider.
func NewGhCLI() *GhCLI {
	return &GhCLI{Binary: "gh"}
}

// CanResolve reports whether ref is a GitHub CLI reference (gh:// prefix).
func (g *GhCLI) CanResolve(ref string) bool {
	return strings.HasPrefix(ref, ghRefPrefix)
}

// Resolve shells out to `gh auth token` to retrieve the logged-in account's
// token, honoring an optional hostname segment for non-github.com hosts.
func (g *GhCLI) Resolve(ref string) (string, error) {
	m := ghRefPattern.FindStringSubmatch(ref)
	if m == nil {
		return "", errors.WithDetails("invalid secret reference: must be gh://token or gh://<hostname>/token",
			"ref", ref)
	}
	args := []string{"auth", "token"}
	if host := m[1]; host != "" {
		args = append(args, "--hostname", host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var out []byte
	var err error
	if g.runner != nil {
		out, err = g.runner(ctx, g.Binary, args...)
	} else {
		out, err = exec.CommandContext(ctx, g.Binary, args...).Output() // #nosec G204 -- binary is a static default, args match a whitelisted grammar
	}
	if err != nil {
		// .Output() stashes the command's stderr on *exec.ExitError; surfacing
		// it turns an opaque "exit status 1" into the actual gh diagnostic
		// (e.g. "not logged in to any hosts").
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return "", errors.WrapWithDetails(err, "resolving GitHub token via gh CLI",
				"ref", ref, "stderr", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", errors.WrapWithDetails(err, "resolving GitHub token via gh CLI", "ref", ref)
	}

	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.WithDetails("gh CLI returned an empty token", "ref", ref)
	}
	return token, nil
}
