package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/gitrepo"
)

// realPath resolves the symlinks a temp dir carries on macOS (/var → /private/var),
// so a path git printed and a path the test joined compare as the same place.
func realPath(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("resolving %q: %v", p, err)
	}
	return resolved
}

// TestWorktreeGitDir_ProjectIsItselfAWorktree is the SC-4595 regression: a
// daemon registered on a linked worktree launched every run with that
// worktree's .git POINTER FILE as the container's only git bind, so the run
// worktree's own pointer led to an unmounted path and every git command in the
// container answered "not a git repository". The bind must be the common dir —
// a real directory, holding the metadata of both worktrees.
func TestWorktreeGitDir_ProjectIsItselfAWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()

	main := filepath.Join(root, "main")
	runGitM(t, root, "init", "-b", "main", main)
	writeFileT(t, filepath.Join(main, "a.txt"), "one\n")
	runGitM(t, main, "add", "a.txt")
	runGitM(t, main, "commit", "-m", "base")

	// The registered project: a linked worktree of main, exactly the shape the
	// daemon runs on when several projects share one repo.
	project := filepath.Join(root, "project")
	runGitM(t, main, "worktree", "add", "--detach", project)

	// The per-run worktree, cut from the project dir the way mountSourceForRun
	// cuts it. Its .git points into the COMMON dir, not into the project dir.
	run := filepath.Join(root, "run")
	runGitM(t, project, "worktree", "add", "--detach", run)

	got := worktreeGitDir(context.Background(), project, run)

	wantCommon := realPath(t, filepath.Join(main, ".git"))
	if realPath(t, got) != wantCommon {
		t.Errorf("gitDir = %q, want the common dir %q", got, wantCommon)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat %q: %v", got, err)
	}
	if !info.IsDir() {
		t.Errorf("gitDir %q is not a directory — a pointer file mounts nothing", got)
	}
	// The bind has to cover what the run worktree's pointer actually names,
	// which is the whole point of preferring the common dir.
	pointer, err := os.ReadFile(filepath.Join(run, ".git")) // #nosec G304 -- test-owned temp path
	if err != nil {
		t.Fatalf("reading run worktree pointer: %v", err)
	}
	target := realPath(t, string(pointer[len("gitdir: "):len(pointer)-1]))
	if rel, relErr := filepath.Rel(realPath(t, got), target); relErr != nil || rel == ".." || filepath.IsAbs(rel) {
		t.Errorf("run worktree gitdir %q is not inside the bind %q", target, got)
	}
}

// TestWorktreeGitDir_MainCheckoutUnchanged pins the case that already worked:
// a project dir that is a main checkout binds its own .git, as before.
func TestWorktreeGitDir_MainCheckoutUnchanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()

	project := filepath.Join(root, "project")
	runGitM(t, root, "init", "-b", "main", project)
	writeFileT(t, filepath.Join(project, "a.txt"), "one\n")
	runGitM(t, project, "add", "a.txt")
	runGitM(t, project, "commit", "-m", "base")

	run := filepath.Join(root, "run")
	runGitM(t, project, "worktree", "add", "--detach", run)

	got := worktreeGitDir(context.Background(), project, run)
	if realPath(t, got) != realPath(t, filepath.Join(project, ".git")) {
		t.Errorf("gitDir = %q, want %q", got, filepath.Join(project, ".git"))
	}
}

func TestWorktreeGitDir_NoWorktreeBindsNothing(t *testing.T) {
	if got := worktreeGitDir(context.Background(), "/some/project", ""); got != "" {
		t.Errorf("gitDir = %q, want empty for a shared-checkout run", got)
	}
}

// TestWorktreeGitDir_FallsBackWhenGitCannotAnswer keeps a launch git cannot
// answer for behaving as it did before the common-dir resolution existed,
// rather than dropping the mount and breaking the main-checkout case too.
func TestWorktreeGitDir_FallsBackWhenGitCannotAnswer(t *testing.T) {
	prev := gitrepo.CommonGitDir
	gitrepo.CommonGitDir = func(_ context.Context, _ string) (string, error) {
		return "", errors.WithDetails("git unavailable")
	}
	t.Cleanup(func() { gitrepo.CommonGitDir = prev })

	got := worktreeGitDir(context.Background(), "/some/project", "/some/run")
	if got != filepath.Join("/some/project", ".git") {
		t.Errorf("gitDir = %q, want the joined fallback", got)
	}
}
