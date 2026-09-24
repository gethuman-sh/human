package cmddaemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/gitrepo"
)

// rebaseGitStubs stubs every gitrepo entry point EnsureMergeable touches and
// records the calls, so the tests can assert the freshness rebase never
// operates on the live workspace checkout (SC-1000).
type rebaseGitStubs struct {
	t *testing.T
	// call log entries: "<op> <dir> [args...]"
	calls []string
	// worktree path handed out by WorktreeAdd, to verify later ops target it
	worktreePath string

	branchOnRemote bool
	localTip       string
	remoteTip      string
	ancestorAfter  bool
	rebaseErr      error
	// behindOrigin drives the SC-2322 behind-publish guard: when set, the
	// rebased tip is reported as strictly behind origin so refuseIfBehind must
	// abort before the lease push. Default false keeps the guard a no-op so the
	// existing rebase-path assertions are unaffected.
	behindOrigin bool
	// branchLocal drives adoptPublishedTip's CAS: whether the branch has a local
	// ref to read the "expected" value from. updateRefErr models a writer that
	// moved the ref concurrently (SC-5395).
	branchLocal  bool
	updateRefErr error
}

func (s *rebaseGitStubs) install() {
	prevDefault, prevFetch, prevExistsRemote, prevRevParse := gitrepo.DefaultBranch, gitrepo.Fetch, gitrepo.BranchExistsRemote, gitrepo.RevParse
	prevIsAncestor, prevAdd, prevRemove := gitrepo.IsAncestor, gitrepo.WorktreeAdd, gitrepo.WorktreeRemove
	prevRebaseHead, prevPushHead, prevPushHeadLease := gitrepo.RebaseHead, gitrepo.PushHead, gitrepo.PushHeadWithLease
	prevCommitsBetween := gitrepo.CommitsBetween
	prevExistsLocal, prevUpdateRef := gitrepo.BranchExistsLocal, gitrepo.UpdateBranchRef
	s.t.Cleanup(func() {
		gitrepo.DefaultBranch, gitrepo.Fetch, gitrepo.BranchExistsRemote, gitrepo.RevParse = prevDefault, prevFetch, prevExistsRemote, prevRevParse
		gitrepo.IsAncestor, gitrepo.WorktreeAdd, gitrepo.WorktreeRemove = prevIsAncestor, prevAdd, prevRemove
		gitrepo.RebaseHead, gitrepo.PushHead, gitrepo.PushHeadWithLease = prevRebaseHead, prevPushHead, prevPushHeadLease
		gitrepo.CommitsBetween = prevCommitsBetween
		gitrepo.BranchExistsLocal, gitrepo.UpdateBranchRef = prevExistsLocal, prevUpdateRef
	})

	gitrepo.DefaultBranch = func(_ context.Context, _ string) string { return "main" }
	gitrepo.Fetch = func(_ context.Context, dir, branch string) error {
		s.calls = append(s.calls, "fetch "+dir+" "+branch)
		return nil
	}
	gitrepo.BranchExistsRemote = func(_ context.Context, _, _ string) bool { return s.branchOnRemote }
	gitrepo.RevParse = func(_ context.Context, dir, rev string) (string, error) {
		s.calls = append(s.calls, "rev-parse "+dir+" "+rev)
		switch {
		case rev == "HEAD":
			return "rebasedtip", nil
		case strings.HasPrefix(rev, "origin/"):
			return s.remoteTip, nil
		}
		return s.localTip, nil
	}
	gitrepo.IsAncestor = func(_ context.Context, _, ancestor, descendant string) bool {
		// Mergeability probe: is the base tip an ancestor of the rebased tip.
		if descendant == "rebasedtip" {
			return s.ancestorAfter
		}
		// Behind probe (SC-2322): is the rebased source an ancestor of origin,
		// i.e. strictly behind it.
		if ancestor == "rebasedtip" {
			return s.behindOrigin
		}
		return false
	}
	gitrepo.CommitsBetween = func(_ context.Context, _, _, _ string) ([]gitrepo.Commit, error) {
		return []gitrepo.Commit{{ShortSHA: "beef", Subject: "newer origin work"}}, nil
	}
	gitrepo.WorktreeAdd = func(_ context.Context, repoDir, worktreePath, base string) error {
		s.worktreePath = worktreePath
		s.calls = append(s.calls, "worktree-add "+repoDir+" "+base)
		return nil
	}
	gitrepo.WorktreeRemove = func(_ context.Context, _, worktreePath string) error {
		s.calls = append(s.calls, "worktree-remove "+worktreePath)
		return nil
	}
	gitrepo.RebaseHead = func(_ context.Context, dir, base, _, _ string) error {
		s.calls = append(s.calls, "rebase "+dir+" "+base)
		return s.rebaseErr
	}
	gitrepo.PushHead = func(_ context.Context, dir, branch string) error {
		s.calls = append(s.calls, "push-head "+dir+" "+branch)
		return nil
	}
	gitrepo.PushHeadWithLease = func(_ context.Context, dir, branch, expected string) error {
		s.calls = append(s.calls, "push-head-lease "+dir+" "+branch+" "+expected)
		return nil
	}
	gitrepo.BranchExistsLocal = func(_ context.Context, _, _ string) bool { return s.branchLocal }
	gitrepo.UpdateBranchRef = func(_ context.Context, dir, branch, newSHA, expectedSHA string) error {
		s.calls = append(s.calls, "update-ref "+dir+" "+branch+" "+newSHA+" "+expectedSHA)
		return s.updateRefErr
	}
}

func (s *rebaseGitStubs) sawCall(prefix string) bool {
	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

const rebaseTestWorkspace = "/live/checkout"

func ensureMergeable(t *testing.T, s *rebaseGitStubs) error {
	_, err := ensureMergeableHead(t, s)
	return err
}

func ensureMergeableHead(t *testing.T, s *rebaseGitStubs) (string, error) {
	t.Helper()
	s.t = t
	s.install()
	return forgeDeployer{}.EnsureMergeable(context.Background(), daemon.PRRequest{
		WorkspaceDir: rebaseTestWorkspace,
		Branch:       "autofix/999",
	})
}

func TestEnsureMergeable_currentBranchSkipsRebase(t *testing.T) {
	s := &rebaseGitStubs{branchOnRemote: true, remoteTip: "rebasedtip", ancestorAfter: true}
	if err := ensureMergeable(t, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.sawCall("worktree-add") || s.sawCall("rebase") {
		t.Errorf("branch already containing the base tip must not rebase, calls: %v", s.calls)
	}
}

func TestEnsureMergeable_rebasesInEphemeralWorktreeNotWorkspace(t *testing.T) {
	s := &rebaseGitStubs{branchOnRemote: true, remoteTip: "staletip", ancestorAfter: true}
	if err := ensureMergeable(t, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !s.sawCall("worktree-add " + rebaseTestWorkspace + " staletip") {
		t.Errorf("expected detached worktree at the origin tip, calls: %v", s.calls)
	}
	if s.worktreePath == rebaseTestWorkspace || s.worktreePath == "" {
		t.Fatalf("worktree path must be an ephemeral dir, got %q", s.worktreePath)
	}
	if !s.sawCall("rebase " + s.worktreePath + " origin/main") {
		t.Errorf("rebase must run in the ephemeral worktree, calls: %v", s.calls)
	}
	if s.sawCall("rebase " + rebaseTestWorkspace) {
		t.Errorf("rebase must never run in the live workspace checkout, calls: %v", s.calls)
	}
	if !s.sawCall("push-head-lease " + s.worktreePath + " autofix/999 staletip") {
		t.Errorf("rebased tip must lease-push from the worktree against the recorded remote tip, calls: %v", s.calls)
	}
	if !s.sawCall("worktree-remove " + s.worktreePath) {
		t.Errorf("ephemeral worktree must be removed, calls: %v", s.calls)
	}
}

func TestEnsureMergeable_conflictStillRemovesWorktree(t *testing.T) {
	s := &rebaseGitStubs{branchOnRemote: true, remoteTip: "staletip", rebaseErr: errors.WithDetails("conflict")}
	if err := ensureMergeable(t, s); err == nil {
		t.Fatal("expected the rebase conflict to fail the deploy")
	}
	if !s.sawCall("worktree-remove") {
		t.Errorf("ephemeral worktree must be removed after a conflict, calls: %v", s.calls)
	}
	if s.sawCall("push-head") {
		t.Errorf("a failed rebase must not push, calls: %v", s.calls)
	}
}

func TestEnsureMergeable_localOnlyBranchPushesPlain(t *testing.T) {
	s := &rebaseGitStubs{branchOnRemote: false, localTip: "localtip", ancestorAfter: true}
	if err := ensureMergeable(t, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !s.sawCall("worktree-add " + rebaseTestWorkspace + " localtip") {
		t.Errorf("local-only branch must rebase from the local tip, calls: %v", s.calls)
	}
	if !s.sawCall("push-head " + s.worktreePath + " autofix/999") {
		t.Errorf("local-only branch must plain-push (no lease target), calls: %v", s.calls)
	}
}

func TestEnsureMergeable_stillBehindAfterRebaseFails(t *testing.T) {
	s := &rebaseGitStubs{branchOnRemote: true, remoteTip: "staletip", ancestorAfter: false}
	if err := ensureMergeable(t, s); err == nil {
		t.Fatal("expected an error when the rebased tip still lacks the base")
	}
}

// TestEnsureMergeable_RefusesBehindPublish proves the sibling publish site
// honours the same never-publish-behind-origin invariant as pushBranch: when
// the rebased tip is strictly behind origin, EnsureMergeable must fail and must
// NOT lease-push the stale tip over the newer origin work (SC-2322).
func TestEnsureMergeable_RefusesBehindPublish(t *testing.T) {
	s := &rebaseGitStubs{branchOnRemote: true, remoteTip: "staletip", ancestorAfter: true, behindOrigin: true}
	if err := ensureMergeable(t, s); err == nil {
		t.Fatal("expected a behind-origin publish to fail the deploy")
	}
	if s.sawCall("push-head-lease") {
		t.Errorf("a source behind origin must not be lease-pushed, calls: %v", s.calls)
	}
}

// TestEnsureMergeable_movesLocalRefToPublishedTip is the SC-5395 regression for
// defect C: after the ephemeral-worktree rebase publishes to origin, this
// machine's local ref must be moved to the published tip too, with the
// existing local value as the CAS expected.
func TestEnsureMergeable_movesLocalRefToPublishedTip(t *testing.T) {
	s := &rebaseGitStubs{branchOnRemote: true, branchLocal: true, remoteTip: "staletip", localTip: "localstale", ancestorAfter: true}
	head, err := ensureMergeableHead(t, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if head != "rebasedtip" {
		t.Errorf("expected the published head, got %q", head)
	}
	if !s.sawCall("update-ref " + rebaseTestWorkspace + " autofix/999 rebasedtip localstale") {
		t.Errorf("expected the local ref moved with the local tip as CAS expected, calls: %v", s.calls)
	}
}

// TestEnsureMergeable_createsMissingLocalRef: a branch known only on origin has
// no local ref to CAS against — the empty expected creates it.
func TestEnsureMergeable_createsMissingLocalRef(t *testing.T) {
	s := &rebaseGitStubs{branchOnRemote: true, branchLocal: false, remoteTip: "staletip", ancestorAfter: true}
	if _, err := ensureMergeableHead(t, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !s.sawCall("update-ref " + rebaseTestWorkspace + " autofix/999 rebasedtip ") {
		t.Errorf("expected the local ref created with an empty CAS expected, calls: %v", s.calls)
	}
}

// TestEnsureMergeable_currentBranchLeavesLocalRefAlone: a branch that already
// contains the base tip is not rebased, so nothing publishes and the local ref
// must not be touched.
func TestEnsureMergeable_currentBranchLeavesLocalRefAlone(t *testing.T) {
	s := &rebaseGitStubs{branchOnRemote: true, branchLocal: true, remoteTip: "rebasedtip", ancestorAfter: true}
	head, err := ensureMergeableHead(t, s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if head != "" {
		t.Errorf("an already-current branch must report no published head, got %q", head)
	}
	if s.sawCall("update-ref") {
		t.Errorf("an already-current branch must not move the local ref, calls: %v", s.calls)
	}
}

// TestEnsureMergeable_localRefMovedConcurrently_IsReported: a refused CAS means
// another writer owns the local ref right now (in practice a deploy-fixer
// committing on the branch) and its commits are not in what was published —
// the error must be reported, not swallowed, and the published head is
// returned alongside it so the caller still knows what was published (SC-5395).
func TestEnsureMergeable_localRefMovedConcurrently_IsReported(t *testing.T) {
	s := &rebaseGitStubs{branchOnRemote: true, branchLocal: true, remoteTip: "staletip", localTip: "localstale",
		ancestorAfter: true, updateRefErr: errors.WithDetails("ref moved")}
	head, err := ensureMergeableHead(t, s)
	if err == nil {
		t.Fatal("expected a concurrently-moved local ref to fail the deploy")
	}
	if head != "rebasedtip" {
		t.Errorf("the published head must still be reported alongside the error, got %q", head)
	}
}

// runGit is the real-git test helper: it runs git with a deterministic identity
// and fails the test on any error, so setup reads as a script.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}
	out, err := exec.Command("git", append(base, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestEnsureMergeable_realGit_dirtyWorkspaceUntouched drives the full freshness
// rebase against a real repository: the workspace checkout is DIRTY (the exact
// condition that used to fail every deploy) and must stay bit-for-bit untouched
// while the stale handoff branch is rebased onto the advanced base and
// published to origin (SC-1000).
func TestEnsureMergeable_realGit_dirtyWorkspaceUntouched(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	runGit(t, root, "init", "--bare", "-b", "main", origin)
	ws := filepath.Join(root, "ws")
	runGit(t, root, "clone", origin, ws)

	// Base commit on main.
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "a.txt")
	runGit(t, ws, "commit", "-m", "base")
	runGit(t, ws, "push", "-u", "origin", "main")

	// Handoff branch with its own commit, pushed, then main advances past it.
	runGit(t, ws, "checkout", "-b", "autofix/x")
	if err := os.WriteFile(filepath.Join(ws, "fix.txt"), []byte("fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "fix.txt")
	runGit(t, ws, "commit", "-m", "fix")
	runGit(t, ws, "push", "origin", "autofix/x")
	runGit(t, ws, "checkout", "main")
	if err := os.WriteFile(filepath.Join(ws, "b.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "b.txt")
	runGit(t, ws, "commit", "-m", "advance")
	runGit(t, ws, "push", "origin", "main")

	// The user's live state: HEAD on main with an uncommitted modification.
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("uncommitted edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	head, err := forgeDeployer{}.EnsureMergeable(context.Background(), daemon.PRRequest{WorkspaceDir: ws, Branch: "autofix/x"})
	if err != nil {
		t.Fatalf("EnsureMergeable on a dirty workspace must succeed, got: %v", err)
	}
	if head == "" {
		t.Error("a stale branch that was rebased and re-pushed must report the published head")
	}

	// The published branch contains the advanced base.
	runGit(t, ws, "fetch", "origin")
	mainTip := runGit(t, ws, "rev-parse", "origin/main")
	if out, e := exec.Command("git", "-C", ws, "merge-base", "--is-ancestor", mainTip, "origin/autofix/x").CombinedOutput(); e != nil {
		t.Errorf("origin/autofix/x must contain the base tip after the deploy rebase: %s", out)
	}

	// The user's checkout is untouched: same branch, same dirty edit.
	if head := runGit(t, ws, "rev-parse", "--abbrev-ref", "HEAD"); head != "main" {
		t.Errorf("workspace HEAD moved to %q — the deploy hijacked the checkout", head)
	}
	content, err := os.ReadFile(filepath.Join(ws, "a.txt"))
	if err != nil || string(content) != "uncommitted edit\n" {
		t.Errorf("uncommitted user edit was disturbed: %q (err %v)", content, err)
	}
}

// TestEnsureMergeable_realGit_rebaseSelfContainedIdentity proves the freshness
// rebase carries its own committer identity and does not depend on an ambient
// git identity (SC-1135). The pipeline agents run in an ephemeral/headless
// checkout with no global git config, so a `git rebase` that replays a commit
// with no configured identity dies with "please tell me who you are" — which
// surfaced as a spurious red suite that failed a correct fix. Here every
// ambient identity source is neutralized (empty GIT_CONFIG_GLOBAL,
// GIT_CONFIG_NOSYSTEM=1); only runGit's inline `-c` identity keeps the setup
// commits working, so the internal rebase is the sole invocation exercised for
// identity. Before the fix the internal rebase fails; after it, the stale branch
// is rebased onto the advanced base and published.
func TestEnsureMergeable_realGit_rebaseSelfContainedIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()

	// Neutralize every ambient git identity source: an empty global config file
	// and no system config. The internal rebase must supply its own identity.
	emptyGlobal := filepath.Join(root, "empty-gitconfig")
	if err := os.WriteFile(emptyGlobal, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", emptyGlobal)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	origin := filepath.Join(root, "origin.git")
	runGit(t, root, "init", "--bare", "-b", "main", origin)
	ws := filepath.Join(root, "ws")
	runGit(t, root, "clone", origin, ws)

	// Base commit on main.
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "a.txt")
	runGit(t, ws, "commit", "-m", "base")
	runGit(t, ws, "push", "-u", "origin", "main")

	// Handoff branch with its own commit, pushed, then main advances past it so
	// the deploy must replay the branch commit onto the advanced base.
	runGit(t, ws, "checkout", "-b", "autofix/x")
	if err := os.WriteFile(filepath.Join(ws, "fix.txt"), []byte("fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "fix.txt")
	runGit(t, ws, "commit", "-m", "fix")
	runGit(t, ws, "push", "origin", "autofix/x")
	runGit(t, ws, "checkout", "main")
	if err := os.WriteFile(filepath.Join(ws, "b.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "b.txt")
	runGit(t, ws, "commit", "-m", "advance")
	runGit(t, ws, "push", "origin", "main")

	head, err := forgeDeployer{}.EnsureMergeable(context.Background(), daemon.PRRequest{WorkspaceDir: ws, Branch: "autofix/x"})
	if err != nil {
		t.Fatalf("EnsureMergeable must not depend on an ambient git identity, got: %v", err)
	}
	if head == "" {
		t.Error("a stale branch that was rebased and re-pushed must report the published head")
	}

	// The published branch contains the advanced base tip.
	runGit(t, ws, "fetch", "origin")
	mainTip := runGit(t, ws, "rev-parse", "origin/main")
	if out, e := exec.Command("git", "-C", ws, "merge-base", "--is-ancestor", mainTip, "origin/autofix/x").CombinedOutput(); e != nil {
		t.Errorf("origin/autofix/x must contain the base tip after the deploy rebase: %s", out)
	}
}

// TestEnsureMergeable_realGit_localRefFollowsThePublishedTip is the SC-5395
// defect-C reproduction: before the fix, the local ref stayed at the
// pre-rebase tip while origin held the rebase, so a following FreshenBranch
// (which reads the LOCAL ref first) merged the base into work that no longer
// existed, and a following push force-pushed that stale merge back over the
// published rebase. After the fix the local ref follows the publish, so
// FreshenBranch and pushBranch both build on what was actually published.
func TestEnsureMergeable_realGit_localRefFollowsThePublishedTip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	runGit(t, root, "init", "--bare", "-b", "main", origin)
	ws := filepath.Join(root, "ws")
	runGit(t, root, "clone", origin, ws)

	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "a.txt")
	runGit(t, ws, "commit", "-m", "base")
	runGit(t, ws, "push", "-u", "origin", "main")

	runGit(t, ws, "checkout", "-b", "autofix/x")
	if err := os.WriteFile(filepath.Join(ws, "fix.txt"), []byte("fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "fix.txt")
	runGit(t, ws, "commit", "-m", "fix")
	runGit(t, ws, "push", "origin", "autofix/x")
	// Workspace left on main, exactly as the daemon leaves it between deploys.
	runGit(t, ws, "checkout", "main")

	if err := os.WriteFile(filepath.Join(ws, "b.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "b.txt")
	runGit(t, ws, "commit", "-m", "advance")
	runGit(t, ws, "push", "origin", "main")

	head, err := forgeDeployer{}.EnsureMergeable(context.Background(), daemon.PRRequest{WorkspaceDir: ws, Branch: "autofix/x"})
	if err != nil {
		t.Fatalf("EnsureMergeable must succeed, got: %v", err)
	}
	if head == "" {
		t.Fatal("a stale branch that was rebased and re-pushed must report the published head")
	}

	runGit(t, ws, "fetch", "origin")
	if got := runGit(t, ws, "rev-parse", "autofix/x"); got != head {
		t.Errorf("local ref must follow the published tip: local=%s published=%s", got, head)
	}
	if got := runGit(t, ws, "rev-parse", "origin/autofix/x"); got != head {
		t.Errorf("origin ref must be the published tip: origin=%s published=%s", got, head)
	}

	// Advance main once more so the follow-up freshen has something to merge.
	if err := os.WriteFile(filepath.Join(ws, "c.txt"), []byte("three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "c.txt")
	runGit(t, ws, "commit", "-m", "advance again")
	runGit(t, ws, "push", "origin", "main")

	prevFastTier := fastTierRunner
	fastTierRunner = func(context.Context, string) (bool, error) { return true, nil }
	t.Cleanup(func() { fastTierRunner = prevFastTier })

	freshness, err := forgeDeployer{}.FreshenBranch(context.Background(), daemon.PRRequest{WorkspaceDir: ws, Branch: "autofix/x"})
	if err != nil {
		t.Fatalf("FreshenBranch must succeed, got: %v", err)
	}
	if freshness != daemon.FreshnessMerged {
		t.Fatalf("expected FreshnessMerged, got %v", freshness)
	}
	if out, e := exec.Command("git", "-C", ws, "merge-base", "--is-ancestor", head, "autofix/x").CombinedOutput(); e != nil {
		t.Errorf("the published rebase must be contained in the freshened branch, not orphaned: %s", out)
	}

	pushErr := forgeDeployer{}.pushBranch(context.Background(), ws, "autofix/x")
	if pushErr != nil {
		t.Fatalf("pushBranch must succeed, got: %v", pushErr)
	}
	runGit(t, ws, "fetch", "origin")
	if out, e := exec.Command("git", "-C", ws, "merge-base", "--is-ancestor", head, "origin/autofix/x").CombinedOutput(); e != nil {
		t.Errorf("the next publish must not overwrite the published rebase: %s", out)
	}
}
