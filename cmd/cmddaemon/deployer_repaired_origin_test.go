package cmddaemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gethuman-sh/human/internal/daemon"
)

// repairedOriginRepo builds the state a person leaves behind when they follow a
// red deploy card's instruction: the branch is REPAIRED on the forge (rebased
// onto the advanced base and force-pushed from their own clone) while the
// project checkout's local ref still holds the pre-repair head. The two are
// diverged, not behind, and only origin contains the current base tip.
func repairedOriginRepo(t *testing.T) (ws, branch, repaired, stale string) {
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

	branch = "feat"
	runGit(t, ws, "checkout", "-b", branch)
	write(t, ws, "a.txt", "one\nbranch\n")
	runGit(t, ws, "add", "a.txt")
	runGit(t, ws, "commit", "-m", "fix")
	runGit(t, ws, "push", "origin", branch)
	stale = runGit(t, ws, "rev-parse", "HEAD")

	// main advances with a conflicting edit to the same file: this is what the
	// deploy's freshen hits on the stale ref, and what the person repaired.
	runGit(t, ws, "checkout", "main")
	write(t, ws, "a.txt", "one\nmain\n")
	runGit(t, ws, "add", "a.txt")
	runGit(t, ws, "commit", "-m", "advance")
	runGit(t, ws, "push", "origin", "main")
	// The daemon leaves the workspace on the default branch between deploys.

	// The person's own clone: the repair, force-pushed to origin.
	human := filepath.Join(root, "human")
	runGit(t, root, "clone", origin, human)
	runGit(t, human, "checkout", "-B", branch, "origin/"+branch)
	runGit(t, human, "reset", "--hard", "origin/main")
	write(t, human, "a.txt", "one\nmain\nbranch\n")
	runGit(t, human, "add", "a.txt")
	runGit(t, human, "commit", "-m", "fix, repaired on the forge")
	runGit(t, human, "push", "--force", "origin", branch)
	repaired = runGit(t, human, "rev-parse", "HEAD")
	// The project checkout still has the pre-repair ref; fetching the remote
	// tracking ref is what the deploy itself does.
	runGit(t, ws, "fetch", "origin")
	return ws, branch, repaired, stale
}

// TestPushBranch_AdoptsTheRepairFromTheForge is the SC-5596 reproduction. The
// local ref is a ghost of a branch the person repaired on origin, exactly as
// the red card told them to. Today pushBranch lease-pushes against the origin
// SHA it read moments earlier, so the lease authorises its own push and the
// repair is force-pushed away ("+ <repair>...<stale> feat -> feat (forced
// update)"). It must adopt the repair instead: origin untouched, the local ref
// moved to it, nothing published.
func TestPushBranch_AdoptsTheRepairFromTheForge(t *testing.T) {
	requireGit(t)
	ws, branch, repaired, stale := repairedOriginRepo(t)

	published, err := forgeDeployer{}.pushBranch(context.Background(), ws, branch)
	if err != nil {
		t.Fatalf("adopting a repaired origin tip must not fail the deploy, got: %v", err)
	}
	if published {
		t.Error("nothing was published: the repair was adopted")
	}
	runGit(t, ws, "fetch", "origin")
	if tip := runGit(t, ws, "rev-parse", "origin/"+branch); tip != repaired {
		t.Errorf("origin/%s = %s, want the repair %s (stale local head is %s)", branch, tip, repaired, stale)
	}
	if tip := runGit(t, ws, "rev-parse", branch); tip != repaired {
		t.Errorf("the local ref must adopt the repair: %s = %s, want %s", branch, tip, repaired)
	}
}

// The second half of the symptom: FreshenBranch reads the LOCAL ref first, so
// on the stale ghost it merges the advanced base into work that no longer
// exists, reports FreshnessConflict and reds the card on a conflict the real
// branch does not have. Reading the repair, it finds the branch already
// current.
func TestFreshenBranch_ReadsTheRepairNotTheStaleLocalRef(t *testing.T) {
	requireGit(t)
	ws, branch, repaired, _ := repairedOriginRepo(t)
	prev := fastTierRunner
	fastTierRunner = func(context.Context, string) (bool, error) { return true, nil }
	t.Cleanup(func() { fastTierRunner = prev })

	got, err := forgeDeployer{}.FreshenBranch(context.Background(),
		daemon.PRRequest{WorkspaceDir: ws, Branch: branch})
	if err != nil {
		t.Fatalf("freshening a repaired branch must not error, got: %v", err)
	}
	if got != daemon.FreshnessCurrent {
		t.Fatalf("freshness = %v, want FreshnessCurrent: the repair already contains the base", got)
	}
	if tip := runGit(t, ws, "rev-parse", branch); tip != repaired {
		t.Errorf("the local ref must have adopted the repair: %s = %s, want %s", branch, tip, repaired)
	}
}

// Neither side is a ghost: each carries a change the other does not, and both
// contain the base. Nothing mechanical can choose, so the publish refuses and
// names both heads — it never deletes either side's work.
func TestPushBranch_RefusesWhenBothSidesCarryUniqueWork(t *testing.T) {
	requireGit(t)
	ws, branch, repaired, _ := repairedOriginRepo(t)
	// Origin gains a commit on top of the repair, from the person's clone.
	human := filepath.Join(filepath.Dir(ws), "human")
	write(t, human, "o.txt", "origin only\n")
	runGit(t, human, "add", "o.txt")
	runGit(t, human, "commit", "-m", "only on origin")
	runGit(t, human, "push", "origin", branch)
	originTip := runGit(t, human, "rev-parse", "HEAD")
	// This machine's ref is the repair plus a commit of its own.
	runGit(t, ws, "update-ref", "refs/heads/"+branch, repaired)
	runGit(t, ws, "checkout", branch)
	runGit(t, ws, "reset", "--hard", repaired)
	write(t, ws, "l.txt", "local only\n")
	runGit(t, ws, "add", "l.txt")
	runGit(t, ws, "commit", "-m", "only on this machine")
	localTip := runGit(t, ws, "rev-parse", "HEAD")
	runGit(t, ws, "checkout", "main")
	runGit(t, ws, "fetch", "origin")

	_, err := forgeDeployer{}.pushBranch(context.Background(), ws, branch)
	if err == nil {
		t.Fatal("a divergence where each side carries unique work must be refused")
	}
	for _, want := range []string{"only on origin", "only on this machine"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name both sides' commits, missing %q: %v", want, err)
		}
	}
	runGit(t, ws, "fetch", "origin")
	if tip := runGit(t, ws, "rev-parse", "origin/"+branch); tip != originTip {
		t.Errorf("origin/%s = %s, want it untouched at %s", branch, tip, originTip)
	}
	if tip := runGit(t, ws, "rev-parse", branch); tip != localTip {
		t.Errorf("the local ref must be untouched at %s, got %s", localTip, tip)
	}
}
