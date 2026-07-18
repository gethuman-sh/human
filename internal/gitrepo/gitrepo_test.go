package gitrepo

import (
	"context"
	"errors"
	"testing"
)

func withRunner(t *testing.T, fn func(ctx context.Context, name string, args ...string) ([]byte, error)) {
	t.Helper()
	prev := runner
	runner = fn
	t.Cleanup(func() { runner = prev })
}

func TestOriginURL_success(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte("https://github.com/octocat/hello-world.git\n"), nil
	})

	url, err := OriginURL(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://github.com/octocat/hello-world.git" {
		t.Errorf("url = %q, want trimmed origin", url)
	}
	want := []string{"git", "-C", "/repo", "remote", "get-url", "origin"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, gotArgs[i], want[i])
		}
	}
}

func TestOriginURL_commandError(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("exit status 128")
	})
	if _, err := OriginURL(context.Background(), "."); err == nil {
		t.Fatal("expected error when git fails")
	}
}

func TestOriginURL_empty(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("  \n"), nil
	})
	if _, err := OriginURL(context.Background(), "."); err == nil {
		t.Fatal("expected error when origin is empty")
	}
}

func TestPush_success(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, nil
	})
	if err := Push(context.Background(), "/repo", "feat/x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"git", "-C", "/repo", "push", "origin", "feat/x"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, gotArgs[i], want[i])
		}
	}
}

func TestPush_error(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("rejected")
	})
	if err := Push(context.Background(), "/repo", "feat/x"); err == nil {
		t.Fatal("expected error when push fails")
	}
}

func TestDefaultBranch_resolved(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("refs/remotes/origin/develop\n"), nil
	})
	if got := DefaultBranch(context.Background(), "/repo"); got != "develop" {
		t.Errorf("DefaultBranch = %q, want develop", got)
	}
}

func TestDefaultBranch_fallback(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("no origin/HEAD")
	})
	if got := DefaultBranch(context.Background(), "/repo"); got != "main" {
		t.Errorf("DefaultBranch = %q, want main fallback", got)
	}
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsRepo_TrueFalse(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte("true\n"), nil
	})
	if !IsRepo(context.Background(), "/repo") {
		t.Error("IsRepo = false, want true")
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/repo", "rev-parse", "--is-inside-work-tree"})

	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("not a repo")
	})
	if IsRepo(context.Background(), "/plain") {
		t.Error("IsRepo = true, want false when git fails")
	}
}

func TestWorktreeAdd_argv(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, nil
	})
	if err := WorktreeAdd(context.Background(), "/repo", "/wt", "main"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/repo", "worktree", "add", "--detach", "/wt", "main"})
}

func TestWorktreeAdd_error(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("already checked out")
	})
	if err := WorktreeAdd(context.Background(), "/repo", "/wt", "main"); err == nil {
		t.Fatal("expected error when worktree add fails")
	}
}

func TestWorktreeRemove_argv(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, nil
	})
	if err := WorktreeRemove(context.Background(), "/repo", "/wt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/repo", "worktree", "remove", "--force", "/wt"})
}

func TestWorktreeRemove_error(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("no such worktree")
	})
	if err := WorktreeRemove(context.Background(), "/repo", "/wt"); err == nil {
		t.Fatal("expected error when worktree remove fails")
	}
}
