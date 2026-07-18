package agent

import (
	"context"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/gitrepo"
)

// stubGit points the gitrepo package vars at in-memory fakes and restores them.
// isRepo controls IsRepo; adds records every worktree path created so the test
// can assert distinctness.
func stubGit(t *testing.T, isRepo bool) *[]string {
	t.Helper()
	var added []string
	prevIsRepo, prevAdd, prevRemove, prevDefault := gitrepo.IsRepo, gitrepo.WorktreeAdd, gitrepo.WorktreeRemove, gitrepo.DefaultBranch
	gitrepo.IsRepo = func(_ context.Context, _ string) bool { return isRepo }
	gitrepo.DefaultBranch = func(_ context.Context, _ string) string { return "main" }
	gitrepo.WorktreeAdd = func(_ context.Context, _, worktreePath, _ string) error {
		added = append(added, worktreePath)
		return nil
	}
	gitrepo.WorktreeRemove = func(_ context.Context, _, _ string) error { return nil }
	t.Cleanup(func() {
		gitrepo.IsRepo, gitrepo.WorktreeAdd, gitrepo.WorktreeRemove, gitrepo.DefaultBranch = prevIsRepo, prevAdd, prevRemove, prevDefault
	})
	return &added
}

// Regression for SC-411: two agents launched in ONE project must resolve to
// DISTINCT bind-mount sources. Before the worktree fix both resolve to the
// shared project dir (identical) and this fails.
func TestMountSourceForRun_ConcurrentAgentsDistinct(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubGit(t, true)
	m := &Manager{}
	proj := "/proj"

	a, err := m.mountSourceForRun(context.Background(), proj, "agent-a")
	if err != nil {
		t.Fatalf("agent-a: %v", err)
	}
	b, err := m.mountSourceForRun(context.Background(), proj, "agent-b")
	if err != nil {
		t.Fatalf("agent-b: %v", err)
	}

	if a == b {
		t.Fatalf("concurrent agents share a mount source %q — git state will corrupt", a)
	}
	if a == proj || b == proj {
		t.Fatalf("mount source must be a private worktree, not the shared project dir (a=%q b=%q proj=%q)", a, b, proj)
	}
}

// A non-git workspace has no corruption hazard: mount the dir directly.
func TestMountSourceForRun_NonGitFallsBackToDir(t *testing.T) {
	stubGit(t, false)
	m := &Manager{}
	got, err := m.mountSourceForRun(context.Background(), "/plain", "agent-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/plain" {
		t.Fatalf("non-git workspace = %q, want the dir itself", got)
	}
}

// A worktree-add failure must propagate, leaving no partial mount source.
func TestMountSourceForRun_AddErrorPropagates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubGit(t, true)
	gitrepo.WorktreeAdd = func(_ context.Context, _, _, _ string) error {
		return context.Canceled
	}
	m := &Manager{}
	got, err := m.mountSourceForRun(context.Background(), "/proj", "agent-a")
	if err == nil {
		t.Fatal("expected error when worktree add fails")
	}
	if got != "" {
		t.Fatalf("mount source = %q, want empty on error", got)
	}
}

// stubWorktreeRemove records every (repo, worktree) removal request.
func stubWorktreeRemove(t *testing.T) *[][2]string {
	t.Helper()
	var calls [][2]string
	prev := gitrepo.WorktreeRemove
	gitrepo.WorktreeRemove = func(_ context.Context, repo, wt string) error {
		calls = append(calls, [2]string{repo, wt})
		return nil
	}
	t.Cleanup(func() { gitrepo.WorktreeRemove = prev })
	return &calls
}

// A completed run's private worktree is removed at the stop choke point.
func TestStopLocked_CompletedRemovesWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := stubWorktreeRemove(t)

	if err := WriteMeta(Meta{
		Name: "wt-done", ContainerID: "cid", ContainerName: ContainerName("wt-done"),
		Status: StatusStopped, ProjectDir: "/proj", Worktree: "/wt/wt-done-abc",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	mgr := &Manager{Docker: &mockDockerClient{}}
	if err := mgr.Stop(context.Background(), "wt-done"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0] != [2]string{"/proj", "/wt/wt-done-abc"} {
		t.Fatalf("WorktreeRemove calls = %v, want one (/proj, /wt/wt-done-abc)", *calls)
	}
}

// A reaped/failed run's worktree is KEPT for forensics — not removed on stop.
func TestStopLocked_ReapedKeepsWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := stubWorktreeRemove(t)

	if err := WriteMeta(Meta{
		Name: "wt-reaped", ContainerID: "cid", ContainerName: ContainerName("wt-reaped"),
		Status: StatusFailed, ProjectDir: "/proj", Worktree: "/wt/wt-reaped-abc",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	mgr := &Manager{Docker: &mockDockerClient{}}
	if err := mgr.Stop(context.Background(), "wt-reaped"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("WorktreeRemove calls = %v, want none for a reaped run", *calls)
	}
}
