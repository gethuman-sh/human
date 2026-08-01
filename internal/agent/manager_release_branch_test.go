package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// runGitM runs git in dir with a deterministic identity, failing the test on
// any error so setup reads as a script.
func runGitM(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}
	out, err := exec.Command("git", append(base, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStopLocked_KeptWorktreeReleasesBranch proves that a run which finished
// WITHOUT a handoff keeps its forensic worktree (files intact for 90 days /
// resume) but no longer OWNS its branch: the worktree is detached so the shared
// repo can fast-forward refs/heads/<branch>. Before the fix the worktree stayed
// attached, freezing the branch ref, so fetch-into-branch is refused and a later
// deploy republishes the frozen tip over newer origin work (SC-2322).
func TestStopLocked_KeptWorktreeReleasesBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	runGitM(t, root, "init", "--bare", "-b", "main", origin)

	// The shared project repo the run's worktree was cut from.
	project := filepath.Join(root, "project")
	runGitM(t, root, "clone", origin, project)
	writeFileT(t, filepath.Join(project, "a.txt"), "one\n")
	runGitM(t, project, "add", "a.txt")
	runGitM(t, project, "commit", "-m", "base")
	runGitM(t, project, "push", "-u", "origin", "main")

	// The run's private worktree checked out on autofix/x at an OLD tip.
	branch := "autofix/x"
	wt := filepath.Join(root, "wt")
	runGitM(t, project, "worktree", "add", "-b", branch, wt)
	writeFileT(t, filepath.Join(wt, "fix.txt"), "fix\n")
	runGitM(t, wt, "add", "fix.txt")
	runGitM(t, wt, "commit", "-m", "fix")
	oldTip := trimNL(runGitM(t, wt, "rev-parse", "HEAD"))
	runGitM(t, wt, "push", "origin", branch)
	// An untracked forensic artifact that must survive the stop untouched.
	writeFileT(t, filepath.Join(wt, "scratch.txt"), "uncommitted forensic note\n")

	// Origin's branch then advances past the run (newer work that must survive).
	runGitM(t, project, "fetch", "origin")
	runGitM(t, project, "checkout", "-B", "advance", "origin/"+branch)
	writeFileT(t, filepath.Join(project, "more.txt"), "newer\n")
	runGitM(t, project, "add", "more.txt")
	runGitM(t, project, "commit", "-m", "newer work on origin")
	runGitM(t, project, "push", "origin", "HEAD:"+branch)
	newTip := trimNL(runGitM(t, project, "rev-parse", "HEAD"))
	runGitM(t, project, "checkout", "main")

	if err := WriteMeta(Meta{
		Name:       "kept-run",
		Status:     StatusRunning,
		CreatedAt:  time.Now(),
		ProjectDir: project,
		Worktree:   wt,
		Handoff:    false, // no handoff => worktree KEPT
	}); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{Docker: &mockDockerClient{}}
	if err := mgr.Stop(context.Background(), "kept-run"); err != nil {
		t.Fatal(err)
	}

	// Forensic files stay exactly as inspectable: committed fix and the
	// untracked scratch note are both present and unchanged.
	if _, err := os.Stat(filepath.Join(wt, "fix.txt")); err != nil {
		t.Errorf("committed forensic file was removed: %v", err)
	}
	scratch, err := os.ReadFile(filepath.Join(wt, "scratch.txt"))
	if err != nil || string(scratch) != "uncommitted forensic note\n" {
		t.Errorf("uncommitted forensic note disturbed: %q (err %v)", scratch, err)
	}

	// The branch is released: the shared repo can now fast-forward its local
	// refs/heads/<branch> to origin's newer tip. Before the fix the worktree
	// still owned the branch and this fetch is refused.
	if out, e := exec.Command("git", "-C", project, "fetch", "origin",
		branch+":refs/heads/"+branch).CombinedOutput(); e != nil {
		t.Fatalf("shared repo could not fast-forward the released branch: %v\n%s", e, out)
	}
	localTip := trimNL(runGitM(t, project, "rev-parse", "refs/heads/"+branch))
	if localTip != newTip {
		t.Errorf("local branch = %s, want origin's newer tip %s (old was %s)", localTip, newTip, oldTip)
	}
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
