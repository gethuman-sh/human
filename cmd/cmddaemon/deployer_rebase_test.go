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
}

func (s *rebaseGitStubs) install() {
	prevDefault, prevFetch, prevExistsRemote, prevRevParse := gitrepo.DefaultBranch, gitrepo.Fetch, gitrepo.BranchExistsRemote, gitrepo.RevParse
	prevIsAncestor, prevAdd, prevRemove := gitrepo.IsAncestor, gitrepo.WorktreeAdd, gitrepo.WorktreeRemove
	prevRebaseHead, prevPushHead, prevPushHeadLease := gitrepo.RebaseHead, gitrepo.PushHead, gitrepo.PushHeadWithLease
	s.t.Cleanup(func() {
		gitrepo.DefaultBranch, gitrepo.Fetch, gitrepo.BranchExistsRemote, gitrepo.RevParse = prevDefault, prevFetch, prevExistsRemote, prevRevParse
		gitrepo.IsAncestor, gitrepo.WorktreeAdd, gitrepo.WorktreeRemove = prevIsAncestor, prevAdd, prevRemove
		gitrepo.RebaseHead, gitrepo.PushHead, gitrepo.PushHeadWithLease = prevRebaseHead, prevPushHead, prevPushHeadLease
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
	gitrepo.IsAncestor = func(_ context.Context, _, _, descendant string) bool {
		if descendant == "rebasedtip" {
			return s.ancestorAfter
		}
		return false
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
	gitrepo.RebaseHead = func(_ context.Context, dir, base string) error {
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
	if !s.sawCall("worktree-add "+rebaseTestWorkspace+" staletip") {
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
	if !s.sawCall("worktree-add "+rebaseTestWorkspace+" localtip") {
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

	err := forgeDeployer{}.EnsureMergeable(context.Background(), daemon.PRRequest{WorkspaceDir: ws, Branch: "autofix/x"})
	if err != nil {
		t.Fatalf("EnsureMergeable on a dirty workspace must succeed, got: %v", err)
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
