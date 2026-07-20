package gitrepo

import (
	"context"
	"errors"
	"strings"
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

func TestBranchExistsLocal_true(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte("deadbeef\n"), nil
	})
	if !BranchExistsLocal(context.Background(), "/repo", "autofix/sc-1") {
		t.Error("BranchExistsLocal = false, want true when the ref resolves")
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/repo", "rev-parse", "--verify", "--quiet", "refs/heads/autofix/sc-1"})
}

func TestBranchExistsLocal_absent(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	})
	if BranchExistsLocal(context.Background(), "/repo", "autofix/sc-1") {
		t.Error("BranchExistsLocal = true, want false when git reports no such ref")
	}
}

func TestBranchExistsLocal_emptyBranch(t *testing.T) {
	called := false
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if BranchExistsLocal(context.Background(), "/repo", "") {
		t.Error("BranchExistsLocal = true, want false for an empty branch")
	}
	if called {
		t.Error("git must not be invoked for an empty branch")
	}
}

func TestBranchExistsRemote_true(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte("deadbeef\trefs/heads/autofix/sc-1\n"), nil
	})
	if !BranchExistsRemote(context.Background(), "/repo", "autofix/sc-1") {
		t.Error("BranchExistsRemote = false, want true when origin has the branch")
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/repo", "ls-remote", "--heads", "origin", "autofix/sc-1"})
}

func TestBranchExistsRemote_absentEmptyOutput(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("\n"), nil
	})
	if BranchExistsRemote(context.Background(), "/repo", "autofix/sc-1") {
		t.Error("BranchExistsRemote = true, want false when ls-remote yields no ref")
	}
}

func TestBranchReachable_localOnly(t *testing.T) {
	var calls int
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		// The local probe resolves, so ls-remote must never be consulted.
		if len(args) > 3 && args[2] == "ls-remote" {
			t.Errorf("ls-remote consulted after a local hit: %v", args)
		}
		return []byte("deadbeef\n"), nil
	})
	if !BranchReachable(context.Background(), "/repo", "autofix/sc-1") {
		t.Error("BranchReachable = false, want true when the branch is local")
	}
	if calls != 1 {
		t.Errorf("git invoked %d times, want 1 (local hit short-circuits)", calls)
	}
}

func TestBranchReachable_neither(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		// Local rev-parse fails; remote ls-remote returns no ref.
		if len(args) > 3 && args[2] == "rev-parse" {
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	})
	if BranchReachable(context.Background(), "/repo", "autofix/sc-1") {
		t.Error("BranchReachable = true, want false when neither local nor origin has the branch")
	}
}

func TestFetch_argv(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, nil
	})
	if err := Fetch(context.Background(), "/repo", "main"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/repo", "fetch", "origin", "main"})
}

func TestFetch_error(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("network down")
	})
	if err := Fetch(context.Background(), "/repo", "main"); err == nil {
		t.Fatal("expected error when fetch fails")
	}
}

func TestIsAncestor_TrueFalse(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, nil
	})
	if !IsAncestor(context.Background(), "/repo", "origin/main", "feat/x") {
		t.Error("IsAncestor = false, want true when git exits 0")
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/repo", "merge-base", "--is-ancestor", "origin/main", "feat/x"})

	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	})
	if IsAncestor(context.Background(), "/repo", "origin/main", "feat/x") {
		t.Error("IsAncestor = true, want false when git reports not-an-ancestor")
	}
}

func TestRevParse_success(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte("deadbeefcafe\n"), nil
	})
	sha, err := RevParse(context.Background(), "/repo", "origin/feat/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "deadbeefcafe" {
		t.Errorf("sha = %q, want trimmed SHA", sha)
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/repo", "rev-parse", "origin/feat/x"})
}

func TestRevParse_error(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("unknown revision")
	})
	if _, err := RevParse(context.Background(), "/repo", "nope"); err == nil {
		t.Fatal("expected error when rev-parse fails")
	}
}

func TestPushWithLease_argv(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, nil
	})
	if err := PushWithLease(context.Background(), "/repo", "feat/x", "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/repo", "push", "--force-with-lease=feat/x:abc123", "origin", "feat/x"})
}

func TestPushWithLease_error(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("stale info")
	})
	if err := PushWithLease(context.Background(), "/repo", "feat/x", "abc123"); err == nil {
		t.Fatal("expected error when the lease is stale")
	}
}

func TestCommitReachable_localRef(t *testing.T) {
	var lastArgs []string
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		lastArgs = args
		// The local rev-parse verify resolves, so the ref is the branch itself.
		if len(args) > 2 && args[2] == "rev-parse" && contains(args, "--verify") {
			return []byte("deadbeef\n"), nil
		}
		return nil, nil
	})
	if !CommitReachable(context.Background(), "/repo", "feat/x", "abc123") {
		t.Error("CommitReachable = false, want true when the commit is an ancestor of the local branch")
	}
	assertArgs(t, lastArgs, []string{"-C", "/repo", "merge-base", "--is-ancestor", "abc123", "feat/x"})
}

func TestCommitReachable_originRef(t *testing.T) {
	var lastArgs []string
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		lastArgs = args
		// No local ref: rev-parse verify fails, so the check falls to origin/<branch>.
		if len(args) > 2 && args[2] == "rev-parse" && contains(args, "--verify") {
			return nil, errors.New("no such ref")
		}
		return nil, nil
	})
	if !CommitReachable(context.Background(), "/repo", "feat/x", "abc123") {
		t.Error("CommitReachable = false, want true when reachable from origin/<branch>")
	}
	assertArgs(t, lastArgs, []string{"-C", "/repo", "merge-base", "--is-ancestor", "abc123", "origin/feat/x"})
}

func TestCommitReachable_absent(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[2] == "rev-parse" && contains(args, "--verify") {
			return []byte("deadbeef\n"), nil
		}
		return nil, errors.New("exit status 1")
	})
	if CommitReachable(context.Background(), "/repo", "feat/x", "abc123") {
		t.Error("CommitReachable = true, want false when the commit is not reachable")
	}
}

func TestCommitReachable_emptyInputs(t *testing.T) {
	called := false
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if CommitReachable(context.Background(), "/repo", "", "abc") {
		t.Error("CommitReachable = true, want false for empty branch")
	}
	if CommitReachable(context.Background(), "/repo", "feat/x", "") {
		t.Error("CommitReachable = true, want false for empty sha")
	}
	if called {
		t.Error("git must not be invoked for empty inputs")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
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

func TestRebaseHead_argv(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, nil
	})
	if err := RebaseHead(context.Background(), "/wt", "origin/main"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/wt", "rebase", "origin/main"})
}

func TestRebaseHead_conflictAborts(t *testing.T) {
	var calls [][]string
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if len(args) >= 3 && args[2] == "rebase" && (len(args) < 4 || args[3] != "--abort") {
			return nil, errors.New("conflict")
		}
		return nil, nil
	})
	if err := RebaseHead(context.Background(), "/wt", "origin/main"); err == nil {
		t.Fatal("expected error on rebase conflict")
	}
	var sawAbort bool
	for _, c := range calls {
		if len(c) >= 4 && c[2] == "rebase" && c[3] == "--abort" {
			sawAbort = true
		}
	}
	if !sawAbort {
		t.Error("rebase conflict must abort the in-progress rebase")
	}
}

func TestPushHead_argv(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, nil
	})
	if err := PushHead(context.Background(), "/wt", "feat/x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/wt", "push", "origin", "HEAD:refs/heads/feat/x"})
}

func TestPushHeadWithLease_argv(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return nil, nil
	})
	if err := PushHeadWithLease(context.Background(), "/wt", "feat/x", "cafe12"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertArgs(t, gotArgs, []string{"git", "-C", "/wt", "push", "--force-with-lease=feat/x:cafe12", "origin", "HEAD:refs/heads/feat/x"})
}

func TestPushHead_error(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("rejected")
	})
	if err := PushHead(context.Background(), "/wt", "feat/x"); err == nil {
		t.Fatal("expected error when push fails")
	}
	if err := PushHeadWithLease(context.Background(), "/wt", "feat/x", "cafe12"); err == nil {
		t.Fatal("expected error when lease push fails")
	}
}

func TestCommitsFor_parsesAndFiltersMerges(t *testing.T) {
	out := "aaa1\x1fa1\x1f[SC-57] Add validation\n" +
		"bbb2\x1fb2\x1fMerge pull request #12 from x/y\n" +
		"ccc3\x1fc3\x1fIssue SC-57 follow-up\n"
	var gotArgs []string
	withRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte(out), nil
	})

	commits, err := CommitsFor(context.Background(), "/repo", "SC-57")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("commits = %d, want 2 (merge PR filtered)", len(commits))
	}
	if commits[0].SHA != "aaa1" || commits[0].ShortSHA != "a1" || commits[0].Subject != "[SC-57] Add validation" {
		t.Errorf("first commit = %+v", commits[0])
	}
	if gotArgs[0] != "git" || gotArgs[3] != "log" {
		t.Errorf("args = %v", gotArgs)
	}
}

func TestCommitsFor_emptyOutput(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("\n"), nil
	})
	commits, err := CommitsFor(context.Background(), ".", "42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("commits = %v, want none", commits)
	}
}

func TestCommitsFor_gitError(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("exit status 128")
	})
	if _, err := CommitsFor(context.Background(), ".", "SC-57"); err == nil {
		t.Fatal("expected error when git fails")
	}
}

func TestKeyRefPattern_numericVsPrefixed(t *testing.T) {
	num := keyRefPattern("42")
	if want := `\[#?42\]`; !strings.Contains(num, want) {
		t.Errorf("numeric pattern %q missing %q", num, want)
	}
	pre := keyRefPattern("SC-57")
	if want := `\[SC-57\]`; !strings.Contains(pre, want) {
		t.Errorf("prefixed pattern %q missing %q", pre, want)
	}
	if strings.Contains(pre, `#?SC-57\]`) {
		t.Errorf("prefixed pattern %q must not carry the numeric hash form", pre)
	}
}

func TestCurrentBranch(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte("feature/x\n"), nil
	})
	branch, err := CurrentBranch(context.Background(), ".")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "feature/x" {
		t.Errorf("branch = %q", branch)
	}
}

func TestCurrentBranch_error(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("exit status 128")
	})
	if _, err := CurrentBranch(context.Background(), "."); err == nil {
		t.Fatal("expected error")
	}
}

func TestTicketKeys_prefixedFirstDeduped(t *testing.T) {
	out := "[SC-881] Offer move\nFix typo #42 and #42 again\n[HUM-59] [SC-79] Add validation\nPlain subject\n"
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(out), nil
	})
	keys, err := TicketKeys(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"SC-881", "HUM-59", "SC-79", "42"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("keys[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
}

func TestTicketKeys_pathsAppendedAfterSeparator(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(""), nil
	})
	_, err := TicketKeys(context.Background(), ".", []string{"internal/tracker"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "-- internal/tracker") {
		t.Errorf("args = %v, want path after --", gotArgs)
	}
}

func TestLatestTag_describeWins(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[2] == "describe" {
			return []byte("v0.21.0\n"), nil
		}
		return []byte("v0.99.0\n"), nil
	})
	if tag := LatestTag(context.Background(), "."); tag != "v0.21.0" {
		t.Errorf("tag = %q", tag)
	}
}

func TestLatestTag_fallbackToNewestByDate(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[2] == "describe" {
			return nil, errors.New("no tags reachable")
		}
		return []byte("v0.20.0\nv0.19.0\n"), nil
	})
	if tag := LatestTag(context.Background(), "."); tag != "v0.20.0" {
		t.Errorf("tag = %q", tag)
	}
}

func TestLatestTag_none(t *testing.T) {
	withRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("no tags")
	})
	if tag := LatestTag(context.Background(), "."); tag != "" {
		t.Errorf("tag = %q, want empty", tag)
	}
}

func TestTouchedSince_refRange(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("abc123\n"), nil
	})
	touched, err := TouchedSince(context.Background(), ".", "v0.21.0", []string{"cmd"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !touched {
		t.Error("touched = false, want true")
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "v0.21.0..HEAD") {
		t.Errorf("args = %v", gotArgs)
	}
}

func TestTouchedSince_sinceFallbackAndUntouched(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("\n"), nil
	})
	touched, err := TouchedSince(context.Background(), ".", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if touched {
		t.Error("touched = true, want false")
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--since=30 days ago") {
		t.Errorf("args = %v", gotArgs)
	}
}
