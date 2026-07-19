package agent

import (
	"context"
	"os"
	"path/filepath"
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

// Regression for SC-478: .claude/ is git-ignored, so a fresh worktree carries
// no skills/commands/agents — the agent's slash-command launch (/human-autofix)
// is then an unknown command and every board run dies at startup. The worktree
// must be provisioned with the project's Claude assets; session state
// (worktrees/, codenav/, …) must NOT leak in.
func TestMountSourceForRun_ProvisionsClaudeAssets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubGit(t, true)
	// The real WorktreeAdd materializes the worktree dir; the stub must too so
	// provisioning has a destination.
	gitrepo.WorktreeAdd = func(_ context.Context, _, worktreePath, _ string) error {
		return os.MkdirAll(worktreePath, 0o750)
	}

	proj := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(proj, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/skills/human-autofix/SKILL.md", "autofix skill")
	write(".claude/commands/human-plan.md", "plan command")
	write(".claude/agents/human-bug-fixer.md", "bug fixer agent")
	write(".claude/settings.local.json", "{}")
	write(".claude/worktrees/leak.txt", "session state")
	write(".claude/codenav/index.db", "db")

	m := &Manager{}
	wt, err := m.mountSourceForRun(context.Background(), proj, "agent-a")
	if err != nil {
		t.Fatalf("mountSourceForRun: %v", err)
	}

	for _, rel := range []string{
		".claude/skills/human-autofix/SKILL.md",
		".claude/commands/human-plan.md",
		".claude/agents/human-bug-fixer.md",
		".claude/settings.local.json",
	} {
		if _, err := os.Stat(filepath.Join(wt, rel)); err != nil {
			t.Errorf("worktree missing agent asset %s: %v", rel, err)
		}
	}
	for _, rel := range []string{".claude/worktrees", ".claude/codenav"} {
		if _, err := os.Stat(filepath.Join(wt, rel)); err == nil {
			t.Errorf("session state %s must not leak into the worktree", rel)
		}
	}
}

// A project without a .claude dir (non-board, plain repo) provisions nothing
// and must not fail the launch.
func TestMountSourceForRun_NoClaudeDirIsFine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubGit(t, true)
	gitrepo.WorktreeAdd = func(_ context.Context, _, worktreePath, _ string) error {
		return os.MkdirAll(worktreePath, 0o750)
	}

	m := &Manager{}
	wt, err := m.mountSourceForRun(context.Background(), t.TempDir(), "agent-a")
	if err != nil {
		t.Fatalf("mountSourceForRun: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".claude")); err == nil {
		t.Fatal("no source .claude, yet one appeared in the worktree")
	}
}

// A provisioning failure must fail the launch loudly AND remove the fresh
// worktree — never hand back a half-provisioned workspace whose agent would
// die later with a misleading "exited without completing the stage".
func TestMountSourceForRun_ProvisionFailureCleansUp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubGit(t, true)
	removed := stubWorktreeRemove(t)
	// Materialize the worktree path as a FILE: provisioning cannot create
	// .claude inside it and must error.
	gitrepo.WorktreeAdd = func(_ context.Context, _, worktreePath, _ string) error {
		return os.WriteFile(worktreePath, []byte("not a dir"), 0o600)
	}

	proj := t.TempDir()
	skill := filepath.Join(proj, ".claude", "skills", "s", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Manager{}
	if _, err := m.mountSourceForRun(context.Background(), proj, "agent-a"); err == nil {
		t.Fatal("expected provisioning failure to fail the launch")
	}
	if len(*removed) != 1 {
		t.Fatalf("WorktreeRemove calls = %d, want 1 (failed provision must not leave the worktree behind)", len(*removed))
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

// A run that posted its handoff (success) has its private worktree removed at
// the stop choke point — the work is safely committed on its branch.
func TestStopLocked_CompletedRemovesWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := stubWorktreeRemove(t)

	if err := WriteMeta(Meta{
		Name: "wt-done", ContainerID: "cid", ContainerName: ContainerName("wt-done"),
		Status: StatusStopped, ProjectDir: "/proj", Worktree: "/wt/wt-done-abc",
		Handoff: true, CreatedAt: time.Now(),
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

// Regression for SC-731: a run that exits WITHOUT posting a handoff (clean exit
// 0, no commit — the data-loss case) must KEEP its worktree for forensics, not
// delete the only copy of the uncommitted work. Before the fix stopLocked
// removed the worktree whenever stopReason=="completed", which every
// no-handoff run is, destroying the work.
func TestStopLocked_NoHandoffKeepsWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := stubWorktreeRemove(t)

	if err := WriteMeta(Meta{
		Name: "wt-nohandoff", ContainerID: "cid", ContainerName: ContainerName("wt-nohandoff"),
		Status: StatusStopped, ProjectDir: "/proj", Worktree: "/wt/wt-nohandoff-abc",
		Handoff: false, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	mgr := &Manager{Docker: &mockDockerClient{}}
	if err := mgr.Stop(context.Background(), "wt-nohandoff"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("WorktreeRemove calls = %v, want none for a no-handoff run (work must be preserved)", *calls)
	}
}

// SC-731: MarkHandoff flips the meta's Handoff flag (the worktree-reclaim
// gate), is idempotent, and is a silent no-op when the meta is missing.
func TestMarkHandoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// No-op on a missing meta: must not panic or create anything.
	MarkHandoff("ghost")
	if _, err := ReadMeta("ghost"); err == nil {
		t.Fatal("MarkHandoff must not create a meta for a missing agent")
	}

	if err := WriteMeta(Meta{Name: "hoff", Status: StatusStopped, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	MarkHandoff("hoff")
	MarkHandoff("hoff") // idempotent
	got, err := ReadMeta("hoff")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if !got.Handoff {
		t.Fatal("MarkHandoff did not set Handoff=true")
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
