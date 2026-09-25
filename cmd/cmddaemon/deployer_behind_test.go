package cmddaemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPushBranch_AdoptsOriginWhenTheSourceIsBehind drives pushBranch against a
// real repository where the local source tip is strictly BEHIND origin: origin
// carries newer work the local ref never saw. Origin is a strict superset of the
// local ref here, so reconcileWithOrigin takes it rather than refusing — the
// newer work still survives, which is what SC-2322 protects; it is only
// pointless to refuse a publish that would not have lost anything (SC-5596).
func TestPushBranch_AdoptsOriginWhenTheSourceIsBehind(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	runGit(t, root, "init", "--bare", "-b", "main", origin)
	ws := filepath.Join(root, "ws")
	runGit(t, root, "clone", origin, ws)

	// Base on main.
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "a.txt")
	runGit(t, ws, "commit", "-m", "base")
	runGit(t, ws, "push", "-u", "origin", "main")

	// Branch pushed at an OLD tip.
	branch := "autofix/x"
	runGit(t, ws, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(ws, "fix.txt"), []byte("fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "fix.txt")
	runGit(t, ws, "commit", "-m", "fix")
	runGit(t, ws, "push", "origin", branch)
	oldTip := strings.TrimSpace(runGit(t, ws, "rev-parse", "HEAD"))

	// Origin advances the branch with newer work that must survive; the local
	// ref is then reset back to the OLD tip (a frozen, behind source).
	if err := os.WriteFile(filepath.Join(ws, "newer.txt"), []byte("newer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws, "add", "newer.txt")
	runGit(t, ws, "commit", "-m", "newer work that must survive")
	runGit(t, ws, "push", "origin", branch)
	newTip := strings.TrimSpace(runGit(t, ws, "rev-parse", "HEAD"))
	runGit(t, ws, "reset", "--hard", oldTip)

	published, err := forgeDeployer{}.pushBranch(context.Background(), ws, branch)
	if err != nil {
		t.Fatalf("a source behind origin must be adopted, not refused, got: %v", err)
	}
	if published {
		t.Error("nothing was published: origin already carried the newer work")
	}

	// Origin still holds the newer tip — nothing was overwritten, and the local
	// ref catches up to it instead of staying a stale ghost (SC-5596).
	runGit(t, ws, "fetch", "origin")
	if tip := strings.TrimSpace(runGit(t, ws, "rev-parse", "origin/"+branch)); tip != newTip {
		t.Errorf("origin/%s = %s, want the preserved newer tip %s", branch, tip, newTip)
	}
	if tip := strings.TrimSpace(runGit(t, ws, "rev-parse", branch)); tip != newTip {
		t.Errorf("the local ref must adopt origin: %s = %s, want %s (old tip was %s)", branch, tip, newTip, oldTip)
	}
}
