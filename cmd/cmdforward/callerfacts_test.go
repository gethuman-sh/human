package cmdforward

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/gitrepo"
)

type fakeGit struct {
	branch      string
	branchErr   error
	commits     map[string][]string // branch → derived short SHAs
	anywhere    []gitrepo.Commit
	containing  map[string][]string // sha → branches
	base        string
	derivedFrom []string // (key, branch) pairs DeriveCommits was asked about
}

func (f *fakeGit) git() Git {
	return Git{
		CurrentBranch: func(context.Context, string) (string, error) { return f.branch, f.branchErr },
		DeriveCommits: func(_ context.Context, _, key, branch string, eng []string) ([]string, error) {
			f.derivedFrom = append(f.derivedFrom, key+"@"+branch+"+"+strings.Join(eng, "|"))
			return f.commits[branch], nil
		},
		CommitsAnywhere:    func(context.Context, string, string) ([]gitrepo.Commit, error) { return f.anywhere, nil },
		BranchesContaining: func(_ context.Context, _, sha string) ([]string, error) { return f.containing[sha], nil },
		DefaultBranch:      func(context.Context, string) string { return f.base },
	}
}

func TestWithCallerFacts_handoffPostAppendsBranchAndCommits(t *testing.T) {
	g := &fakeGit{branch: "fix/sc-1", commits: map[string][]string{"fix/sc-1": {"aaa", "bbb"}}}
	got, err := WithCallerFacts(context.Background(), []string{"handoff", "post", "SC-1"}, ".", g.git())
	require.NoError(t, err)
	assert.Equal(t, []string{"handoff", "post", "SC-1", "--branch", "fix/sc-1", "--commits", "aaa,bbb"}, got)
	assert.Equal(t, []string{"SC-1@fix/sc-1+"}, g.derivedFrom, "commits are anchored at the caller's branch")
}

func TestWithCallerFacts_handoffPostHonoursEngineeringKeys(t *testing.T) {
	g := &fakeGit{branch: "fix/sc-1", commits: map[string][]string{"fix/sc-1": {"aaa"}}}
	_, err := WithCallerFacts(context.Background(), []string{"handoff", "post", "SC-1", "--engineering", "HUM-1,HUM-2"}, ".", g.git())
	require.NoError(t, err)
	assert.Equal(t, []string{"SC-1@fix/sc-1+HUM-1|HUM-2"}, g.derivedFrom)
}

func TestWithCallerFacts_handoffPostExplicitFlagsLeftAlone(t *testing.T) {
	g := &fakeGit{branch: "main"}
	for _, args := range [][]string{
		{"handoff", "post", "SC-1", "--branch", "feat/x", "--commits", "abc"},
		{"handoff", "post", "SC-1", "--branch=feat/x", "--commits=abc"},
	} {
		got, err := WithCallerFacts(context.Background(), args, ".", g.git())
		require.NoError(t, err)
		assert.Equal(t, args, got)
		assert.Empty(t, g.derivedFrom)
	}
}

func TestWithCallerFacts_handoffPostExplicitBranchStillDerivesCommitsThere(t *testing.T) {
	g := &fakeGit{branch: "main", commits: map[string][]string{"feat/x": {"abc"}}}
	got, err := WithCallerFacts(context.Background(), []string{"handoff", "post", "SC-1", "--branch", "feat/x"}, ".", g.git())
	require.NoError(t, err)
	assert.Equal(t, []string{"handoff", "post", "SC-1", "--branch", "feat/x", "--commits", "abc"}, got)
	assert.Equal(t, []string{"SC-1@feat/x+"}, g.derivedFrom)
}

func TestWithCallerFacts_handoffPostDetachedHeadRefused(t *testing.T) {
	g := &fakeGit{branch: "HEAD"}
	_, err := WithCallerFacts(context.Background(), []string{"handoff", "post", "SC-1"}, ".", g.git())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detached HEAD")
}

func TestWithCallerFacts_handoffPostNoCommitsRefused(t *testing.T) {
	g := &fakeGit{branch: "fix/sc-1"}
	_, err := WithCallerFacts(context.Background(), []string{"handoff", "post", "SC-1"}, ".", g.git())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no commits reference the work keys")
}

func TestWithCallerFacts_handoffPostGitErrorSurfaces(t *testing.T) {
	g := &fakeGit{branchErr: errors.New("not a git repository")}
	_, err := WithCallerFacts(context.Background(), []string{"handoff", "post", "SC-1"}, ".", g.git())
	require.Error(t, err)
}

func TestWithCallerFacts_globalValueFlagIsNotTheVerb(t *testing.T) {
	g := &fakeGit{branch: "fix/sc-1", commits: map[string][]string{"fix/sc-1": {"aaa"}}}
	got, err := WithCallerFacts(context.Background(), []string{"--tracker", "handoff", "--verbose", "handoff", "post", "SC-1"}, ".", g.git())
	require.NoError(t, err)
	assert.Equal(t, "aaa", got[len(got)-1])
}

func TestWithCallerFacts_otherCommandsUntouched(t *testing.T) {
	g := &fakeGit{branch: "fix/sc-1"}
	for _, args := range [][]string{
		{"handoff", "show", "SC-1"},
		{"get", "SC-1"},
		{"--", "handoff", "post", "SC-1"},
		{"handoff", "post"},
		nil,
	} {
		got, err := WithCallerFacts(context.Background(), args, ".", g.git())
		require.NoError(t, err)
		assert.Equal(t, args, got)
	}
}

func TestWithCallerFacts_deployOneCandidateBranch(t *testing.T) {
	g := &fakeGit{
		base:       "main",
		anywhere:   []gitrepo.Commit{{SHA: "1"}, {SHA: "2"}},
		containing: map[string][]string{"1": {"fix/sc-1", "main"}, "2": {"fix/sc-1"}},
	}
	got, err := WithCallerFacts(context.Background(), []string{"deploy", "SC-1"}, ".", g.git())
	require.NoError(t, err)
	assert.Equal(t, []string{"deploy", "SC-1", CandidateBranchesFlag, "fix/sc-1"}, got, "the base branch is never a candidate")
}

func TestWithCallerFacts_deployNoCandidatesStillTellsTheDaemon(t *testing.T) {
	g := &fakeGit{base: "main"}
	got, err := WithCallerFacts(context.Background(), []string{"deploy", "SC-1"}, ".", g.git())
	require.NoError(t, err)
	assert.Equal(t, []string{"deploy", "SC-1", CandidateBranchesFlag, ""}, got)
}

func TestWithCallerFacts_deployTwoCandidatesPassedForTheDaemonToRefuse(t *testing.T) {
	g := &fakeGit{
		base:       "main",
		anywhere:   []gitrepo.Commit{{SHA: "1"}},
		containing: map[string][]string{"1": {"fix/a", "fix/b"}},
	}
	got, err := WithCallerFacts(context.Background(), []string{"deploy", "SC-1", "--ready"}, ".", g.git())
	require.NoError(t, err)
	assert.Equal(t, []string{"deploy", "SC-1", "--ready", CandidateBranchesFlag, "fix/a,fix/b"}, got)
}

func TestWithCallerFacts_deployExplicitBranchLeftAlone(t *testing.T) {
	g := &fakeGit{base: "main", anywhere: []gitrepo.Commit{{SHA: "1"}}, containing: map[string][]string{"1": {"fix/a"}}}
	args := []string{"deploy", "SC-1", "--branch", "release/x"}
	got, err := WithCallerFacts(context.Background(), args, ".", g.git())
	require.NoError(t, err)
	assert.Equal(t, args, got)
}

func TestWithCallerFacts_deployGitFailureLeavesArgsAlone(t *testing.T) {
	g := Git{
		CommitsAnywhere: func(context.Context, string, string) ([]gitrepo.Commit, error) { return nil, errors.New("no git") },
		DefaultBranch:   func(context.Context, string) string { return "main" },
	}
	args := []string{"deploy", "SC-1"}
	got, err := WithCallerFacts(context.Background(), args, ".", g)
	require.NoError(t, err)
	assert.Equal(t, args, got)
}
