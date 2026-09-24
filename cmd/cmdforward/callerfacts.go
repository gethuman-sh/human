// Package cmdforward settles, on the client, the facts a forwarded command
// would otherwise read in the wrong place. `handoff post` and `deploy` are
// answered by the daemon, but the branch a caller stands on and the commits it
// carries are facts about the CALLER's checkout — a worktree, an agent's
// container — and reading them in the daemon's checkout produced "no commits
// reference the work keys" for work that was right there (SC-5330). The
// `capabilities` command runs locally for the same reason; these two cannot,
// because they also post to the tracker with the daemon's credentials, so the
// git half is settled here and travels as explicit flags.
package cmdforward

import (
	"context"
	"strings"

	"github.com/gethuman-sh/human/cmd/cmdhandoff"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/cliflags"
	"github.com/gethuman-sh/human/internal/gitrepo"
)

// CandidateBranchesFlag is the hidden `deploy` flag through which the client
// hands the daemon the branches it found carrying the ticket's commits. The
// daemon uses it only when the ticket has no review handoff, so an explicit
// --branch and a recorded handoff keep precedence over a derivation.
const CandidateBranchesFlag = "--candidate-branches"

// Git is the slice of git the derivation needs, injected so the rewrite is a
// pure function over the argument list in tests.
type Git struct {
	CurrentBranch      func(ctx context.Context, dir string) (string, error)
	DeriveCommits      func(ctx context.Context, dir, key, branch string, engineering []string) ([]string, error)
	CommitsAnywhere    func(ctx context.Context, dir, key string) ([]gitrepo.Commit, error)
	BranchesContaining func(ctx context.Context, dir, sha string) ([]string, error)
	DefaultBranch      func(ctx context.Context, dir string) string
}

// RealGit reads the caller's repository.
func RealGit() Git {
	return Git{
		CurrentBranch:      gitrepo.CurrentBranch,
		DeriveCommits:      cmdhandoff.DeriveCommits,
		CommitsAnywhere:    gitrepo.CommitsAnywhere,
		BranchesContaining: gitrepo.BranchesContaining,
		DefaultBranch:      gitrepo.DefaultBranch,
	}
}

// WithCallerFacts returns args with the caller's git facts appended where the
// command would otherwise derive them daemon-side. Commands other than
// `handoff post` and `deploy`, and invocations that already carry the flags,
// come back untouched. An error is one the caller must see before anything is
// sent: a detached HEAD, or a branch with no commits for the key.
func WithCallerFacts(ctx context.Context, args []string, dir string, git Git) ([]string, error) {
	verb, rest := command(args)
	switch verb {
	case "handoff":
		if len(rest) == 0 || rest[0] != "post" {
			return args, nil
		}
		return handoffFacts(ctx, args, rest[1:], dir, git)
	case "deploy":
		return deployFacts(ctx, args, rest, dir, git)
	default:
		return args, nil
	}
}

// command finds the subcommand after the global flags — the same walk
// isLocalSubcommand does, skipping the value of a space-separated global flag
// so it is never read as the verb.
func command(args []string) (verb string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return "", nil
		}
		if strings.HasPrefix(a, "-") {
			if cliflags.ValueFlags[a] && i+1 < len(args) {
				i++
			}
			continue
		}
		return a, args[i+1:]
	}
	return "", nil
}

// invocation is the parsed tail of a subcommand: its positional key and the
// value-taking flags it was given.
type invocation struct {
	key   string
	flags map[string]string
}

// parse reads the positional key and the named value flags out of rest. Only
// the flags in valueFlags take a value; every other dash token is a boolean
// and consumed alone. The first positional is the key.
func parse(rest []string, valueFlags map[string]bool) invocation {
	inv := invocation{flags: map[string]string{}}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if name, value, ok := strings.Cut(a, "="); ok && strings.HasPrefix(name, "-") {
			inv.flags[name] = value
			continue
		}
		if strings.HasPrefix(a, "-") {
			if valueFlags[a] && i+1 < len(rest) {
				inv.flags[a] = rest[i+1]
				i++
			} else {
				inv.flags[a] = ""
			}
			continue
		}
		if inv.key == "" {
			inv.key = a
		}
	}
	return inv
}

func (inv invocation) has(flag string) bool {
	_, ok := inv.flags[flag]
	return ok
}

var handoffValueFlags = map[string]bool{"--engineering": true, "--branch": true, "--commits": true, "--notes": true, "--review": true}

// handoffFacts appends --branch and --commits from the caller's checkout when
// the invocation carries neither. Both are refused here rather than at the
// daemon when they cannot be settled, because the caller's checkout is the
// only place the answer could have come from.
func handoffFacts(ctx context.Context, args, rest []string, dir string, git Git) ([]string, error) {
	inv := parse(rest, handoffValueFlags)
	if inv.key == "" {
		return args, nil
	}
	branch := inv.flags["--branch"]
	if !inv.has("--branch") {
		derived, err := git.CurrentBranch(ctx, dir)
		if err != nil {
			return nil, err
		}
		if derived == "HEAD" {
			return nil, errors.WithDetails("detached HEAD has no branch — pass --branch", "dir", dir)
		}
		branch = derived
		args = append(args, "--branch", branch)
	}
	if inv.has("--commits") || branch == "" {
		return args, nil
	}
	commits, err := git.DeriveCommits(ctx, dir, inv.key, branch, splitList(inv.flags["--engineering"]))
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return nil, errors.WithDetails("no commits reference the work keys — commit first or pass --commits", "key", inv.key, "branch", branch)
	}
	return append(args, "--commits", strings.Join(commits, ",")), nil
}

var deployValueFlags = map[string]bool{"--branch": true, "--title": true, CandidateBranchesFlag: true}

// deployFacts appends the branches carrying the ticket's commits, for the
// daemon to fall back on when the ticket has no handoff. The base branch is
// excluded: commits reachable from it are already shipped, and naming it would
// make every merged ticket ambiguous with its own branch. Nothing here refuses
// — zero or several candidates are the daemon's to report, beside the handoff
// it alone can read — and a git failure leaves the args alone for the same
// reason: the daemon's own answer must not be blocked by a client that could
// not look.
func deployFacts(ctx context.Context, args, rest []string, dir string, git Git) ([]string, error) {
	inv := parse(rest, deployValueFlags)
	if inv.key == "" || inv.has("--branch") || inv.has(CandidateBranchesFlag) {
		return args, nil
	}
	commits, err := git.CommitsAnywhere(ctx, dir, inv.key)
	if err != nil {
		return args, nil
	}
	base := git.DefaultBranch(ctx, dir)
	var candidates []string
	seen := map[string]bool{}
	for _, c := range commits {
		branches, err := git.BranchesContaining(ctx, dir, c.SHA)
		if err != nil {
			return args, nil
		}
		for _, b := range branches {
			if b == base || seen[b] {
				continue
			}
			seen[b] = true
			candidates = append(candidates, b)
		}
	}
	return append(args, CandidateBranchesFlag, strings.Join(candidates, ",")), nil
}

// splitList splits a comma-separated list, trimming blanks.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
