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
