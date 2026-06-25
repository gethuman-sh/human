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
