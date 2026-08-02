package cmddaemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// resolvedBranchRepo builds the exact state a deploy-fixer leaves behind: origin
// carries the branch at a tip that conflicts with the advanced base, and the
// LOCAL branch ref carries the fixer's resolution — the same commit rebased onto
// current main. It returns the workspace and the resolved local tip.
//
// The fixer's container holds no push credentials, so this divergence between
// the local and origin refs IS the handoff; the daemon publishing it is what the
// deploy sees.
func resolvedBranchRepo(t *testing.T) (ws, branch, resolvedTip string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	runGit(t, root, "init", "--bare", "-b", "main", origin)
	ws = filepath.Join(root, "ws")
	runGit(t, root, "clone", origin, ws)

	write(t, ws, "a.txt", "one\n")
	runGit(t, ws, "add", "a.txt")
	runGit(t, ws, "commit", "-m", "base")
	runGit(t, ws, "push", "-u", "origin", "main")

	// The handoff branch edits a.txt; it is published at that tip.
	branch = "autofix/x"
	runGit(t, ws, "checkout", "-b", branch)
	write(t, ws, "a.txt", "one\nbranch\n")
	runGit(t, ws, "add", "a.txt")
	runGit(t, ws, "commit", "-m", "fix")
	runGit(t, ws, "push", "origin", branch)

	// main advances with a conflicting edit to the same file — the mechanical
	// rebase the deploy runs on the ORIGIN tip cannot resolve this.
	runGit(t, ws, "checkout", "main")
	write(t, ws, "a.txt", "one\nmain\n")
	runGit(t, ws, "add", "a.txt")
	runGit(t, ws, "commit", "-m", "advance")
	runGit(t, ws, "push", "origin", "main")

	// The fixer's resolution: the branch, rebased onto current main with both
	// sides kept, committed on the LOCAL branch ref and never pushed.
	runGit(t, ws, "checkout", branch)
	runGit(t, ws, "reset", "--hard", "origin/main")
	write(t, ws, "a.txt", "one\nmain\nbranch\n")
	runGit(t, ws, "add", "a.txt")
	runGit(t, ws, "commit", "-m", "fix, rebased onto main")
	resolvedTip = runGit(t, ws, "rev-parse", "HEAD")
	return ws, branch, resolvedTip
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// The stranded-resolution regression: a deploy-fixer resolves a conflict on the
// local branch and cannot push (board containers hold no credentials), so
// without a daemon-side publish the deploy re-reads the unresolved ORIGIN tip
// and hits the identical conflict forever. PublishResolvedBranch must carry the
// local resolution to origin (SC-2845).
func TestPublishResolvedBranch_PublishesLocalResolution(t *testing.T) {
	requireGit(t)
	ws, branch, resolved := resolvedBranchRepo(t)

	published, err := forgeDeployer{}.PublishResolvedBranch(context.Background(), ws, branch)
	if err != nil {
		t.Fatalf("publishing a resolved branch must succeed, got: %v", err)
	}
	if !published {
		t.Error("a local ref that resolves the conflict must be reported as published")
	}

	runGit(t, ws, "fetch", "origin")
	if tip := runGit(t, ws, "rev-parse", "origin/"+branch); tip != resolved {
		t.Errorf("origin/%s = %s, want the fixer's resolved tip %s", branch, tip, resolved)
	}
	// The published tip contains the base — which is the whole point: the deploy
	// that follows can merge it without another rebase.
	mainTip := runGit(t, ws, "rev-parse", "origin/main")
	if out, e := exec.Command("git", "-C", ws, "merge-base", "--is-ancestor", mainTip, "origin/"+branch).CombinedOutput(); e != nil {
		t.Errorf("the published branch must contain the base tip: %s", out)
	}
}

// A local ref that does NOT contain the base tip is not the resolution the
// deploy is waiting for — a fixer that reported done without rebasing, or a
// stale ref. Publishing it would ship an unexamined tip over the branch, so it
// is left alone for the deploy's own freshness rebase to handle.
func TestPublishResolvedBranch_LeavesUnresolvedLocalRef(t *testing.T) {
	requireGit(t)
	ws, branch, _ := resolvedBranchRepo(t)
	originTip := runGit(t, ws, "rev-parse", "origin/"+branch)
	// Roll the local ref back to a tip that predates the base advance.
	runGit(t, ws, "reset", "--hard", originTip)

	published, err := forgeDeployer{}.PublishResolvedBranch(context.Background(), ws, branch)
	if err != nil {
		t.Fatalf("an unresolved local ref is not an error, got: %v", err)
	}
	if published {
		t.Error("a local ref that does not contain the base tip must not be published")
	}
	runGit(t, ws, "fetch", "origin")
	if tip := runGit(t, ws, "rev-parse", "origin/"+branch); tip != originTip {
		t.Errorf("origin/%s = %s, want it untouched at %s", branch, tip, originTip)
	}
}

// Re-entry must be a no-op: a resolution already on origin (a retried deploy, a
// second Stop event for the same fixer) is nothing to carry, and reporting it as
// published would claim work that did not happen.
func TestPublishResolvedBranch_AlreadyPublishedIsNoOp(t *testing.T) {
	requireGit(t)
	ws, branch, resolved := resolvedBranchRepo(t)
	if _, err := (forgeDeployer{}).PublishResolvedBranch(context.Background(), ws, branch); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	published, err := forgeDeployer{}.PublishResolvedBranch(context.Background(), ws, branch)
	if err != nil {
		t.Fatalf("re-publishing an already-published resolution must not error, got: %v", err)
	}
	if published {
		t.Error("an already-published resolution must report published=false")
	}
	runGit(t, ws, "fetch", "origin")
	if tip := runGit(t, ws, "rev-parse", "origin/"+branch); tip != resolved {
		t.Errorf("origin/%s = %s, want it unchanged at %s", branch, tip, resolved)
	}
}

// No local branch at all — the fixer never checked one out, or a foreign card's
// branch — is nothing to publish, not a failure. The deploy proceeds on origin.
func TestPublishResolvedBranch_NoLocalBranch(t *testing.T) {
	requireGit(t)
	ws, branch, _ := resolvedBranchRepo(t)
	runGit(t, ws, "checkout", "main")
	runGit(t, ws, "branch", "-D", branch)

	published, err := forgeDeployer{}.PublishResolvedBranch(context.Background(), ws, branch)
	if err != nil {
		t.Fatalf("a missing local branch is not an error, got: %v", err)
	}
	if published {
		t.Error("there is no local resolution to publish")
	}
}

// The publish goes through pushBranch, so it inherits the
// never-publish-behind-origin guard: a resolution that is strictly behind newer
// origin work must be refused, not lease-pushed over it. Without this, carrying
// the fixer's work would become a new way to lose someone else's.
func TestPublishResolvedBranch_RefusesBehindOrigin(t *testing.T) {
	requireGit(t)
	ws, branch, resolved := resolvedBranchRepo(t)
	// The resolution lands on origin, then origin gains newer work on top of it
	// while the local ref stays frozen at the older resolved tip.
	if _, err := (forgeDeployer{}).PublishResolvedBranch(context.Background(), ws, branch); err != nil {
		t.Fatalf("seeding publish: %v", err)
	}
	write(t, ws, "later.txt", "later\n")
	runGit(t, ws, "add", "later.txt")
	runGit(t, ws, "commit", "-m", "newer work that must survive")
	runGit(t, ws, "push", "origin", branch)
	newTip := runGit(t, ws, "rev-parse", "HEAD")
	runGit(t, ws, "reset", "--hard", resolved)

	published, err := forgeDeployer{}.PublishResolvedBranch(context.Background(), ws, branch)
	if err == nil {
		t.Fatal("publishing a resolution behind origin must be refused")
	}
	if published {
		t.Error("a refused publish must not report published=true")
	}
	if !strings.Contains(err.Error(), "newer work that must survive") {
		t.Errorf("the refusal must name the commit it protected, got: %v", err)
	}
	runGit(t, ws, "fetch", "origin")
	if tip := runGit(t, ws, "rev-parse", "origin/"+branch); tip != newTip {
		t.Errorf("origin/%s = %s, want the preserved newer tip %s", branch, tip, newTip)
	}
}
