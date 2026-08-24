// Package gitrepo reads facts about the local git repository by shelling out
// to git. It is the single place that runs git from Go (elsewhere git is only
// invoked from agent prompts), so the exec is isolated and testable.
package gitrepo

import (
	"context"
	"os/exec"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/commitref"
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

// RebaseHead replays the current (detached) HEAD of the worktree at dir onto
// base (running `git -C <dir> rebase <base>`). The deploy's freshness rebase
// runs through this in an ephemeral detached worktree so the live workspace
// checkout — whose dirty state would refuse the rebase and whose HEAD must
// never be hijacked — is not involved (SC-1000). On failure it aborts the
// in-progress rebase so the worktree is never left mid-rebase; the returned
// error signals a real conflict the mechanical path cannot resolve. Package var
// so callers can stub git access in tests.
//
// The replay carries an explicit committer identity via inline `-c` config
// (SC-1135): the pipeline runs this in a headless/ephemeral worktree that has no
// global git identity, so a plain `git rebase` that re-commits the replayed
// commits would die with "please tell me who you are" — an environmental
// failure that used to surface as a spurious red suite and fail a correct fix.
// The name/email are supplied by the caller (the configured bot identity, SC-371)
// so the whole pipeline shares one committer. The identity is set only on the
// replay invocation, never on `--abort` (which commits nothing).
var RebaseHead = func(ctx context.Context, dir, base, name, email string) error {
	if _, err := runner(ctx, "git", "-C", dir,
		"-c", "user.name="+name,
		"-c", "user.email="+email,
		"rebase", base); err != nil {
		// Leaving a half-applied rebase would strand the worktree; abort so a
		// retry starts from a clean state. The abort's own error is irrelevant —
		// the conflict is the failure we report.
		_, _ = runner(ctx, "git", "-C", dir, "rebase", "--abort")
		return errors.WrapWithDetails(err, "rebasing branch onto base", "dir", dir, "base", base)
	}
	return nil
}

// PushHead pushes the current HEAD of the worktree at dir to refs/heads/<branch>
// on origin (running `git -C <dir> push origin HEAD:refs/heads/<branch>`),
// publishing a detached rebase result without ever checking the branch out.
// Package var so callers can stub git access in tests.
var PushHead = func(ctx context.Context, dir, branch string) error {
	if _, err := runner(ctx, "git", "-C", dir, "push", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return errors.WrapWithDetails(err, "pushing HEAD to origin branch", "dir", dir, "branch", branch)
	}
	return nil
}

// PushHeadWithLease force-pushes the current HEAD of the worktree at dir to
// refs/heads/<branch> on origin only if the remote tip still matches
// expectedRemoteSHA. Like PushWithLease, the lease is what lets a rebased tip
// advance a diverged remote WITHOUT clobbering a concurrent push; like
// PushHead, the refspec form publishes a detached HEAD without a branch
// checkout. Package var so callers can stub git access in tests.
var PushHeadWithLease = func(ctx context.Context, dir, branch, expectedRemoteSHA string) error {
	lease := "--force-with-lease=" + branch + ":" + expectedRemoteSHA
	if _, err := runner(ctx, "git", "-C", dir, "push", lease, "origin", "HEAD:refs/heads/"+branch); err != nil {
		return errors.WrapWithDetails(err, "lease-pushing HEAD to origin branch", "dir", dir, "branch", branch, "expected", expectedRemoteSHA)
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

// Reachability is the tri-state outcome of a git reachability probe. A boolean
// cannot say "I could not determine this", so a probe that errored or timed out
// was indistinguishable from a genuine negative — the defect SC-2403 fixes.
// Unknown is the zero value so an unset/failed probe never reads as a clean
// negative a caller might act on.
type Reachability int

const (
	ReachabilityUnknown Reachability = iota // probe could not run to completion (git error, timeout, killed)
	ReachabilityPresent                     // the fact was read as true
	ReachabilityAbsent                      // the fact was read as a clean false (git ran, answered "no")
)

// cleanAbsence reports whether err is git answering a definite "no" — a
// completed process that exited 1 (merge-base --is-ancestor: not an ancestor;
// rev-parse --verify: no such ref). Any other error (exit >1, a killed process,
// a context timeout, a spawn failure) is NOT a clean absence and must read as
// Unknown. .Output() returns the *exec.ExitError unwrapped, so a type assertion
// is sufficient (matches internal/vault/readsecret.go).
func cleanAbsence(err error) bool {
	ee, ok := err.(*exec.ExitError)
	return ok && ee.ExitCode() == 1
}

// CommitReachability is CommitReachable's tri-state form: Present when git
// confirms sha is an ancestor of branch, Absent only when git ran to
// completion and cleanly answered "not an ancestor" (exit 1), and Unknown for
// anything else (a git error, a timeout, a killed process) — none of which may
// be read as evidence the commit is missing. Package var so callers can stub
// git access in tests.
var CommitReachability = func(ctx context.Context, dir, branch, sha string) Reachability {
	// An empty ref/sha is a caller-supplied definite non-match, not an
	// unreadable probe; keep it a clean absence and never shell out.
	if branch == "" || sha == "" {
		return ReachabilityAbsent
	}
	ref := branch
	if !BranchExistsLocal(ctx, dir, branch) {
		ref = "origin/" + branch
	}
	_, err := runner(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", sha, ref)
	switch {
	case err == nil:
		return ReachabilityPresent
	case cleanAbsence(err):
		return ReachabilityAbsent
	default:
		return ReachabilityUnknown
	}
}

// CommitReachable reports whether sha is reachable from branch in the repository
// at dir. It resolves branch as a local ref when present (a board-context fix
// leaves its branch local on the machine that produced it), else as
// origin/<branch>, then runs `git -C <dir> merge-base --is-ancestor <sha> <ref>`.
// An empty branch or sha short-circuits to false. Package var so callers can
// stub git access in tests.
var CommitReachable = func(ctx context.Context, dir, branch, sha string) bool {
	return CommitReachability(ctx, dir, branch, sha) == ReachabilityPresent
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

func branchLocalReachability(ctx context.Context, dir, branch string) Reachability {
	if branch == "" {
		return ReachabilityAbsent
	}
	_, err := runner(ctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	switch {
	case err == nil:
		return ReachabilityPresent
	case cleanAbsence(err):
		return ReachabilityAbsent
	default:
		return ReachabilityUnknown
	}
}

func branchRemoteReachability(ctx context.Context, dir, branch string) Reachability {
	if branch == "" {
		return ReachabilityAbsent
	}
	out, err := runner(ctx, "git", "-C", dir, "ls-remote", "--heads", "origin", branch)
	switch {
	case err != nil:
		return ReachabilityUnknown // ls-remote gives no exit-1 "clean absence"; any error is unreadable
	case strings.TrimSpace(string(out)) == "":
		return ReachabilityAbsent
	default:
		return ReachabilityPresent
	}
}

// BranchReachability is BranchReachable's tri-state form: Present if the branch
// resolves locally or on origin; Unknown if neither confirmed it but a probe
// could not complete (so absence cannot be claimed); Absent only when both
// probes ran and cleanly reported no such ref. The local probe short-circuits a
// remote round-trip on a hit, matching BranchReachable's original behaviour.
var BranchReachability = func(ctx context.Context, dir, branch string) Reachability {
	local := branchLocalReachability(ctx, dir, branch)
	if local == ReachabilityPresent {
		return ReachabilityPresent
	}
	remote := branchRemoteReachability(ctx, dir, branch)
	if remote == ReachabilityPresent {
		return ReachabilityPresent
	}
	if local == ReachabilityUnknown || remote == ReachabilityUnknown {
		return ReachabilityUnknown
	}
	return ReachabilityAbsent
}

// BranchReachable reports whether branch resolves on THIS machine for the
// repository at dir — as a local ref or on origin. The local check comes first so
// a branch left local (a board-context fix on the machine that produced it) is
// reachable without a network round-trip; once the branch is pushed, the remote
// check also passes. Package var so callers can stub git access in tests.
var BranchReachable = func(ctx context.Context, dir, branch string) bool {
	return BranchReachability(ctx, dir, branch) == ReachabilityPresent
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

// WorktreeDetach detaches HEAD in the worktree at worktreePath to its current
// commit (running `git -C <worktreePath> checkout --detach`). A kept forensic
// worktree otherwise keeps OWNING refs/heads/<branch>, which freezes the shared
// repo's local branch so a fetch cannot fast-forward it and a later deploy
// republishes the frozen tip over newer work (SC-2322). Detaching to the SAME
// commit changes only the ref pointer, never the working tree, so every tracked,
// staged, modified, and untracked forensic file stays bit-for-bit as-is (SC-731);
// only the branch ownership is released. `checkout --detach` (not `switch`) for
// compatibility with older git. Package var for test stubs.
var WorktreeDetach = func(ctx context.Context, worktreePath string) error {
	if _, err := runner(ctx, "git", "-C", worktreePath, "checkout", "--detach"); err != nil {
		return errors.WrapWithDetails(err, "detaching git worktree", "worktree", worktreePath)
	}
	return nil
}

// Commit is one commit that references a ticket key, as returned by CommitsFor.
type Commit struct {
	SHA      string `json:"sha"`
	ShortSHA string `json:"short"`
	Subject  string `json:"subject"`
}

// CommitsFor lists the commits on HEAD whose message references key in any
// accepted reference format, newest first, excluding merge-PR commits — the
// exact discovery agents otherwise hand-roll with git log --grep incantations.
// Package var so callers can stub git access in tests.
var CommitsFor = func(ctx context.Context, dir, key string) ([]Commit, error) {
	return CommitsForRev(ctx, dir, key, "HEAD")
}

// CommitsForRev is CommitsFor anchored at an explicit rev instead of HEAD —
// the review handoff derives commits from the handed-off BRANCH, which in a
// board workspace is usually not the checked-out ref. Package var so callers
// can stub git access in tests.
var CommitsForRev = func(ctx context.Context, dir, key, rev string) ([]Commit, error) {
	out, err := runner(ctx, "git", "-C", dir, "log", "--extended-regexp",
		"--grep="+commitref.GrepPattern(key), "--format=%H%x1f%h%x1f%s", rev)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "listing commits for key", "dir", dir, "key", key)
	}
	var commits []Commit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		if strings.HasPrefix(parts[2], "Merge pull request") {
			continue
		}
		commits = append(commits, Commit{SHA: parts[0], ShortSHA: parts[1], Subject: parts[2]})
	}
	return commits, nil
}

// CommitsBetween lists the commits in the range base..to (in to but not in
// base) at dir, newest first — the newer commits a behind publish would
// overwrite, named so the guard's error can point the reader at what to recover
// (SC-2322). Mirrors CommitsForRev's parse of the %H/%h/%s record. Package var
// so callers can stub git access in tests.
var CommitsBetween = func(ctx context.Context, dir, base, to string) ([]Commit, error) {
	out, err := runner(ctx, "git", "-C", dir, "log",
		"--format=%H%x1f%h%x1f%s", base+".."+to)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "listing commits in range", "dir", dir, "base", base, "to", to)
	}
	var commits []Commit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, Commit{SHA: parts[0], ShortSHA: parts[1], Subject: parts[2]})
	}
	return commits, nil
}

// CurrentBranch returns the checked-out branch name of the repository at dir
// (running `git -C <dir> rev-parse --abbrev-ref HEAD`). A detached HEAD yields
// "HEAD", which callers should treat as "no branch". Package var so callers can
// stub git access in tests.
var CurrentBranch = func(ctx context.Context, dir string) (string, error) {
	out, err := runner(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", errors.WrapWithDetails(err, "reading current branch", "dir", dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// TicketKeys extracts the ticket keys referenced by commits touching paths
// (all history when paths is empty): prefixed keys first, then numeric ones,
// each deduped in order of first appearance (newest commit first). Package var
// so callers can stub git access in tests.
var TicketKeys = func(ctx context.Context, dir string, paths []string) ([]string, error) {
	args := []string{"-C", dir, "log", "--format=%s"}
	if len(paths) > 0 {
		args = append(append(args, "--"), paths...)
	}
	out, err := runner(ctx, "git", args...)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "listing commit subjects", "dir", dir)
	}
	return commitref.Keys(strings.Split(string(out), "\n")), nil
}

// LatestTag resolves the recency boundary tag: the nearest reachable tag,
// else the newest tag by creation date, else "" (caller falls back to a time
// window). Package var so callers can stub git access in tests.
var LatestTag = func(ctx context.Context, dir string) string {
	if out, err := runner(ctx, "git", "-C", dir, "describe", "--tags", "--abbrev=0"); err == nil {
		if tag := strings.TrimSpace(string(out)); tag != "" {
			return tag
		}
	}
	if out, err := runner(ctx, "git", "-C", dir, "tag", "--sort=-creatordate"); err == nil {
		if lines := strings.Fields(strings.TrimSpace(string(out))); len(lines) > 0 {
			return lines[0]
		}
	}
	return ""
}

// TouchedSince reports whether any commit after boundary touches paths.
// boundary is a ref (tag) for ref..HEAD, or empty for the fixed 30-day
// fallback window. Package var so callers can stub git access in tests.
var TouchedSince = func(ctx context.Context, dir, boundary string, paths []string) (bool, error) {
	args := []string{"-C", dir, "log", "--format=%H", "-1"}
	if boundary != "" {
		args = append(args, boundary+"..HEAD")
	} else {
		args = append(args, "--since=30 days ago")
	}
	if len(paths) > 0 {
		args = append(append(args, "--"), paths...)
	}
	out, err := runner(ctx, "git", args...)
	if err != nil {
		return false, errors.WrapWithDetails(err, "checking recent commits", "dir", dir, "boundary", boundary)
	}
	return strings.TrimSpace(string(out)) != "", nil
}
