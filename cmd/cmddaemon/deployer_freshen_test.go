package cmddaemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/gitrepo"
)

// freshenGitStubs models the git surface FreshenBranch touches, so the base
// merge's decisions — skip, merge and move the ref, or report a conflict and
// leave the ref alone — can be asserted without a repository.
type freshenGitStubs struct {
	t     *testing.T
	calls []string

	localBranch bool
	localTip    string
	originTip   string
	current     bool
	conflict    bool
	mergeErr    error

	// fastTierFails/fastTierErr stub the post-merge fast-tier check
	// (fastTierRunner). Both default false/nil — a clean pass — so the
	// merge/conflict-focused tests predating SC-5279's fast-tier check need
	// not know it exists.
	fastTierFails bool
	fastTierErr   error
}

func (s *freshenGitStubs) install() {
	prevDefault, prevFetch, prevExistsLocal, prevRevParse := gitrepo.DefaultBranch, gitrepo.Fetch, gitrepo.BranchExistsLocal, gitrepo.RevParse
	prevIsAncestor, prevAdd, prevRemove := gitrepo.IsAncestor, gitrepo.WorktreeAdd, gitrepo.WorktreeRemove
	prevMerge, prevUpdate := gitrepo.MergeIntoHead, gitrepo.UpdateBranchRef
	prevFastTier := fastTierRunner
	s.t.Cleanup(func() {
		gitrepo.DefaultBranch, gitrepo.Fetch, gitrepo.BranchExistsLocal, gitrepo.RevParse = prevDefault, prevFetch, prevExistsLocal, prevRevParse
		gitrepo.IsAncestor, gitrepo.WorktreeAdd, gitrepo.WorktreeRemove = prevIsAncestor, prevAdd, prevRemove
		gitrepo.MergeIntoHead, gitrepo.UpdateBranchRef = prevMerge, prevUpdate
		fastTierRunner = prevFastTier
	})
	fastTierRunner = func(_ context.Context, _ string) (bool, error) {
		s.calls = append(s.calls, "fast-tier")
		if s.fastTierErr != nil {
			return false, s.fastTierErr
		}
		return !s.fastTierFails, nil
	}
	gitrepo.DefaultBranch = func(_ context.Context, _ string) string { return "main" }
	gitrepo.Fetch = func(_ context.Context, _, branch string) error {
		s.calls = append(s.calls, "fetch "+branch)
		return nil
	}
	gitrepo.BranchExistsLocal = func(_ context.Context, _, _ string) bool { return s.localBranch }
	gitrepo.RevParse = func(_ context.Context, _, rev string) (string, error) {
		switch {
		case rev == "HEAD":
			return "mergedtip", nil
		case strings.HasPrefix(rev, "origin/"):
			return s.originTip, nil
		}
		return s.localTip, nil
	}
	gitrepo.IsAncestor = func(_ context.Context, _, _, _ string) bool { return s.current }
	gitrepo.WorktreeAdd = func(_ context.Context, _, _, base string) error {
		s.calls = append(s.calls, "worktree-add "+base)
		return nil
	}
	gitrepo.WorktreeRemove = func(_ context.Context, _, _ string) error {
		s.calls = append(s.calls, "worktree-remove")
		return nil
	}
	gitrepo.MergeIntoHead = func(_ context.Context, _, ref, _, _ string) (bool, error) {
		s.calls = append(s.calls, "merge "+ref)
		return s.conflict, s.mergeErr
	}
	gitrepo.UpdateBranchRef = func(_ context.Context, _, branch, newSHA, expected string) error {
		s.calls = append(s.calls, "update-ref "+branch+" "+newSHA+" "+expected)
		return nil
	}
}

func (s *freshenGitStubs) saw(prefix string) bool {
	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func freshen(t *testing.T, s *freshenGitStubs) (daemon.BranchFreshness, error) {
	t.Helper()
	s.install()
	return forgeDeployer{}.FreshenBranch(context.Background(), daemon.PRRequest{WorkspaceDir: "/live/checkout", Branch: "fix/x"})
}

func TestFreshenBranch_currentBranchIsLeftAlone(t *testing.T) {
	s := &freshenGitStubs{t: t, localBranch: true, localTip: "local1", current: true}
	got, err := freshen(t, s)
	if err != nil || got != daemon.FreshnessCurrent {
		t.Fatalf("got %v, %v", got, err)
	}
	if s.saw("worktree-add") || s.saw("merge") || s.saw("update-ref") {
		t.Fatalf("a branch that contains the base tip must not be touched: %v", s.calls)
	}
}

// The local ref is what the loop's reviewer and fixer read, so it is the ref
// the merge moves — with a compare-and-swap on the tip it started from.
func TestFreshenBranch_mergesBaseAndMovesLocalRef(t *testing.T) {
	s := &freshenGitStubs{t: t, localBranch: true, localTip: "local1"}
	got, err := freshen(t, s)
	if err != nil || got != daemon.FreshnessMerged {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, want := range []string{"fetch main", "worktree-add local1", "merge origin/main", "update-ref fix/x mergedtip local1", "worktree-remove"} {
		if !s.saw(want) {
			t.Errorf("missing %q in %v", want, s.calls)
		}
	}
}

// A branch known only on origin gets a local ref created at the merge, so the
// reviewer's local-first binding finds the integrated head.
func TestFreshenBranch_originOnlyBranchCreatesLocalRef(t *testing.T) {
	s := &freshenGitStubs{t: t, localBranch: false, originTip: "origin1"}
	got, err := freshen(t, s)
	if err != nil || got != daemon.FreshnessMerged {
		t.Fatalf("got %v, %v", got, err)
	}
	if !s.saw("fetch fix/x") || !s.saw("worktree-add origin1") || !s.saw("update-ref fix/x mergedtip ") {
		t.Fatalf("expected an origin fetch and a ref create: %v", s.calls)
	}
}

// A conflict is an outcome, not an error: the ref is untouched and the
// worktree is cleaned up, so the deploy fixer starts from the branch as it was.
func TestFreshenBranch_conflictLeavesRefUntouched(t *testing.T) {
	s := &freshenGitStubs{t: t, localBranch: true, localTip: "local1", conflict: true}
	got, err := freshen(t, s)
	if err != nil || got != daemon.FreshnessConflict {
		t.Fatalf("got %v, %v", got, err)
	}
	if s.saw("update-ref") {
		t.Fatalf("a conflicting merge must not move the ref: %v", s.calls)
	}
	if !s.saw("worktree-remove") {
		t.Fatalf("the ephemeral worktree must be removed: %v", s.calls)
	}
}

// A clean merge that fails the fast tier is a red result, not a reviewable
// candidate: the ref stays untouched exactly like a textual conflict, and the
// caller routes it to the deploy fixer before any reviewer runs (SC-5279
// acceptance criterion 1).
func TestFreshenBranch_fastTierFailureLeavesRefUntouched(t *testing.T) {
	s := &freshenGitStubs{t: t, localBranch: true, localTip: "local1", fastTierFails: true}
	got, err := freshen(t, s)
	if err != nil || got != daemon.FreshnessTestsFailed {
		t.Fatalf("got %v, %v", got, err)
	}
	if !s.saw("merge origin/main") || !s.saw("fast-tier") {
		t.Fatalf("expected the merge and the fast tier to both run: %v", s.calls)
	}
	if s.saw("update-ref") {
		t.Fatalf("a red fast tier must not move the ref: %v", s.calls)
	}
	if !s.saw("worktree-remove") {
		t.Fatalf("the ephemeral worktree must still be removed: %v", s.calls)
	}
}

// The fast tier could not even run (no toolchain, no `make` on the host) —
// that is a tooling failure, not a verdict on the merged code, so it is
// surfaced like any other freshen error: the ref is left untouched and the
// caller falls back to reviewing the branch as it is.
func TestFreshenBranch_fastTierToolingFailureIsAnError(t *testing.T) {
	s := &freshenGitStubs{t: t, localBranch: true, localTip: "local1", fastTierErr: errors.New("exec: \"make\": executable file not found in $PATH")}
	got, err := freshen(t, s)
	if err == nil {
		t.Fatal("a fast tier that could not run must surface as an error")
	}
	if got != daemon.FreshnessCurrent {
		t.Fatalf("got %v", got)
	}
	if s.saw("update-ref") {
		t.Fatalf("no ref move when the fast tier could not run: %v", s.calls)
	}
}

func TestFreshenBranch_gitFailureIsAnError(t *testing.T) {
	s := &freshenGitStubs{t: t, localBranch: true, localTip: "local1", mergeErr: errors.New("boom")}
	if _, err := freshen(t, s); err == nil {
		t.Fatal("a failing merge command must surface as an error")
	}
	if s.saw("update-ref") {
		t.Fatalf("no ref move on error: %v", s.calls)
	}
}
