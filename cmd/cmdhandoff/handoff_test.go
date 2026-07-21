package cmdhandoff

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/gitrepo"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// stubProvider implements tracker.Provider; only the comment methods matter.
type stubProvider struct {
	comments []tracker.Comment
	added    []string
	addErr   error
}

func (s *stubProvider) ListIssues(context.Context, tracker.ListOptions) ([]tracker.Issue, error) {
	return nil, nil
}
func (s *stubProvider) GetIssue(context.Context, string) (*tracker.Issue, error) { return nil, nil }
func (s *stubProvider) CreateIssue(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	return issue, nil
}
func (s *stubProvider) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return s.comments, nil
}
func (s *stubProvider) AddComment(_ context.Context, _, body string) (*tracker.Comment, error) {
	s.added = append(s.added, body)
	return &tracker.Comment{Body: body}, s.addErr
}
func (s *stubProvider) LinkIssues(context.Context, string, string) error      { return nil }
func (s *stubProvider) DeleteIssue(context.Context, string) error             { return nil }
func (s *stubProvider) TransitionIssue(context.Context, string, string) error { return nil }
func (s *stubProvider) AssignIssue(context.Context, string, string) error     { return nil }
func (s *stubProvider) GetCurrentUser(context.Context) (string, error)        { return "", nil }
func (s *stubProvider) EditIssue(context.Context, string, tracker.EditOptions) (*tracker.Issue, error) {
	return nil, nil
}
func (s *stubProvider) ListStatuses(context.Context, string) ([]tracker.Status, error) {
	return nil, nil
}

func stubGit(t *testing.T, branch string, commitsByKey map[string][]gitrepo.Commit, reachable map[string]bool) {
	t.Helper()
	prevBranch, prevCommits, prevReach, prevFetch := gitrepo.CurrentBranch, gitrepo.CommitsForRev, gitrepo.CommitReachable, gitrepo.Fetch
	gitrepo.CurrentBranch = func(context.Context, string) (string, error) { return branch, nil }
	gitrepo.CommitsForRev = func(_ context.Context, _, key, _ string) ([]gitrepo.Commit, error) {
		return commitsByKey[key], nil
	}
	gitrepo.CommitReachable = func(_ context.Context, _, _, sha string) bool { return reachable[sha] }
	gitrepo.Fetch = func(context.Context, string, string) error { return nil }
	t.Cleanup(func() {
		gitrepo.CurrentBranch, gitrepo.CommitsForRev, gitrepo.CommitReachable, gitrepo.Fetch = prevBranch, prevCommits, prevReach, prevFetch
	})
}

func TestRunHandoffPost_derivesEverything(t *testing.T) {
	stubGit(t, "autofix/sc-1", map[string][]gitrepo.Commit{
		"SC-1": {{ShortSHA: "bbb"}, {ShortSHA: "aaa"}}, // git log order: newest first
	}, map[string]bool{"aaa": true, "bbb": true})
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", PostOptions{Verify: true})
	require.NoError(t, err)
	require.Len(t, p.added, 1)
	assert.Equal(t, "[human:ready-for-review]\nbranch: autofix/sc-1\ncommits: aaa, bbb", p.added[0])
}

func TestRunHandoffPost_withNotes(t *testing.T) {
	stubGit(t, "autofix/sc-1", map[string][]gitrepo.Commit{
		"SC-1": {{ShortSHA: "aaa"}},
	}, map[string]bool{"aaa": true})
	p := &stubProvider{}
	var buf bytes.Buffer

	opts := PostOptions{Verify: true, Notes: "Open items: retry flake"}
	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", opts)
	require.NoError(t, err)
	require.Len(t, p.added, 1)
	assert.Equal(t,
		"[human:ready-for-review]\nbranch: autofix/sc-1\ncommits: aaa\n\nOpen items: retry flake",
		p.added[0])

	m, ok := marker.ParseBody(p.added[0])
	require.True(t, ok)
	assert.Equal(t, "autofix/sc-1", m.Fields["branch"])
	assert.Equal(t, "aaa", m.Fields["commits"])
	assert.Equal(t, "Open items: retry flake", m.Body)
}

func TestRunHandoffPost_emptyNotesOmitsBody(t *testing.T) {
	stubGit(t, "autofix/sc-1", map[string][]gitrepo.Commit{
		"SC-1": {{ShortSHA: "aaa"}},
	}, map[string]bool{"aaa": true})
	p := &stubProvider{}
	var buf bytes.Buffer

	opts := PostOptions{Verify: true, Notes: ""}
	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", opts)
	require.NoError(t, err)
	require.Len(t, p.added, 1)
	assert.Equal(t, "[human:ready-for-review]\nbranch: autofix/sc-1\ncommits: aaa", p.added[0])
}

func TestRunHandoffPost_splitTopologyFieldsAndDaemon(t *testing.T) {
	stubGit(t, "main", map[string][]gitrepo.Commit{
		"HUM-89": {{ShortSHA: "abc"}},
		"HUM-90": {{ShortSHA: "def"}},
	}, map[string]bool{"abc": true, "def": true})
	p := &stubProvider{}
	var buf bytes.Buffer

	opts := PostOptions{Engineering: []string{"HUM-89", "HUM-90"}, DaemonID: "d-1", Verify: true}
	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", opts)
	require.NoError(t, err)
	require.Len(t, p.added, 1)
	assert.Equal(t,
		"[human:ready-for-review]\nengineering: HUM-89, HUM-90\nbranch: main\ncommits: abc, def\ndaemon: d-1",
		p.added[0])
}

func TestRunHandoffPost_unreachableCommitBlocks(t *testing.T) {
	stubGit(t, "main", map[string][]gitrepo.Commit{
		"SC-1": {{ShortSHA: "aaa"}},
	}, map[string]bool{})
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", PostOptions{Verify: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not reachable")
	assert.Empty(t, p.added)
}

func TestRunHandoffPost_noVerifySkipsReachability(t *testing.T) {
	stubGit(t, "main", map[string][]gitrepo.Commit{
		"SC-1": {{ShortSHA: "aaa"}},
	}, map[string]bool{})
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", PostOptions{Verify: false})
	require.NoError(t, err)
	require.Len(t, p.added, 1)
}

func TestRunHandoffPost_noCommitsFails(t *testing.T) {
	stubGit(t, "main", map[string][]gitrepo.Commit{}, nil)
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", PostOptions{Verify: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no commits reference")
}

func TestRunHandoffPost_detachedHeadFails(t *testing.T) {
	stubGit(t, "HEAD", nil, nil)
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", PostOptions{Verify: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detached HEAD")
}

func TestRunHandoffPost_explicitOverridesSkipDerivation(t *testing.T) {
	stubGit(t, "wrong-branch", nil, map[string]bool{"x1": true})
	p := &stubProvider{}
	var buf bytes.Buffer

	opts := PostOptions{Branch: "release", Commits: []string{"x1"}, Verify: true}
	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", opts)
	require.NoError(t, err)
	assert.Contains(t, p.added[0], "branch: release")
	assert.Contains(t, p.added[0], "commits: x1")
}

func TestRunHandoffShow_parsesNewestHandoff(t *testing.T) {
	now := time.Now()
	p := &stubProvider{comments: []tracker.Comment{
		{Body: "[human:ready-for-review]\nbranch: old\ncommits: zzz", Created: now.Add(-time.Hour)},
		{Body: "[human:ready-for-review]\nengineering: HUM-89, HUM-90\nbranch: main\ncommits: abc, def\ndaemon: d-1", Created: now},
	}}
	var buf bytes.Buffer

	err := RunHandoffShow(context.Background(), p, &buf, "SC-1")
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, `"engineering": [`)
	assert.Contains(t, out, `"HUM-89"`)
	assert.Contains(t, out, `"branch": "main"`)
	assert.Contains(t, out, `"abc"`)
	assert.Contains(t, out, `"daemon": "d-1"`)
	assert.NotContains(t, out, "zzz", "latest handoff wins")
}

func TestRunHandoffShow_missing(t *testing.T) {
	p := &stubProvider{}
	var buf bytes.Buffer
	err := RunHandoffShow(context.Background(), p, &buf, "SC-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no review handoff")
}

func TestRunHandoffPost_addCommentError(t *testing.T) {
	stubGit(t, "main", map[string][]gitrepo.Commit{"SC-1": {{ShortSHA: "aaa"}}}, map[string]bool{"aaa": true})
	p := &stubProvider{addErr: errors.WithDetails("tracker down")}
	var buf bytes.Buffer
	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", PostOptions{Verify: true})
	require.Error(t, err)
}

func TestSplitList(t *testing.T) {
	assert.Nil(t, splitList(""))
	assert.Nil(t, splitList("  "))
	assert.Equal(t, []string{"a", "b"}, splitList("a, b"))
	assert.Equal(t, []string{"a"}, splitList("a,"))
}

func TestRunHandoffPost_derivesCommitsFromBranchNotHead(t *testing.T) {
	// The 1087 deadlock: the caller's checkout sits on main while the work
	// lives on the branch — derivation must anchor at the handed-off branch.
	var gotRev string
	prevBranch, prevCommits, prevReach, prevFetch := gitrepo.CurrentBranch, gitrepo.CommitsForRev, gitrepo.CommitReachable, gitrepo.Fetch
	gitrepo.CurrentBranch = func(context.Context, string) (string, error) { return "main", nil }
	gitrepo.CommitsForRev = func(_ context.Context, _, _, rev string) ([]gitrepo.Commit, error) {
		gotRev = rev
		return []gitrepo.Commit{{ShortSHA: "abc"}}, nil
	}
	gitrepo.CommitReachable = func(context.Context, string, string, string) bool { return true }
	gitrepo.Fetch = func(context.Context, string, string) error { return nil }
	t.Cleanup(func() {
		gitrepo.CurrentBranch, gitrepo.CommitsForRev, gitrepo.CommitReachable, gitrepo.Fetch = prevBranch, prevCommits, prevReach, prevFetch
	})

	p := &stubProvider{}
	var buf bytes.Buffer
	err := RunHandoffPost(context.Background(), p, &buf, ".", "SC-1", PostOptions{Branch: "feat/sc-1", Verify: true})
	require.NoError(t, err)
	assert.Equal(t, "feat/sc-1", gotRev)
}
