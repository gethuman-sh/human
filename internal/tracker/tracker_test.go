package tracker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubProvider satisfies Provider with no-op methods.
type stubProvider struct{}

// fakeProvider is a Provider whose GetIssue can be configured to succeed or fail.
type fakeProvider struct {
	stubProvider
	getIssueErr error
}

func (f fakeProvider) GetIssue(_ context.Context, _ string) (*Issue, error) {
	if f.getIssueErr != nil {
		return nil, f.getIssueErr
	}
	return &Issue{Key: "found"}, nil
}

func (stubProvider) ListIssues(context.Context, ListOptions) ([]Issue, error)       { return nil, nil }
func (stubProvider) GetIssue(context.Context, string) (*Issue, error)               { return nil, nil }
func (stubProvider) CreateIssue(context.Context, *Issue) (*Issue, error)            { return nil, nil }
func (stubProvider) ListComments(context.Context, string) ([]Comment, error)        { return nil, nil }
func (stubProvider) AddComment(context.Context, string, string) (*Comment, error)   { return nil, nil }
func (stubProvider) DeleteIssue(context.Context, string) error                      { return nil }
func (stubProvider) TransitionIssue(context.Context, string, string) error          { return nil }
func (stubProvider) AssignIssue(context.Context, string, string) error              { return nil }
func (stubProvider) GetCurrentUser(context.Context) (string, error)                 { return "", nil }
func (stubProvider) EditIssue(context.Context, string, EditOptions) (*Issue, error) { return nil, nil }
func (stubProvider) ListStatuses(context.Context, string) ([]Status, error)         { return nil, nil }

func TestResolveByKind_found(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "personal", Kind: "github", Provider: stubProvider{}},
	}

	inst, err := ResolveByKind("github", instances, "")
	require.NoError(t, err)
	assert.Equal(t, "personal", inst.Name)
	assert.Equal(t, "github", inst.Kind)
}

func TestResolveByKind_notFound(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
	}

	_, err := ResolveByKind("github", instances, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no github tracker found")
}

func TestResolveByKind_withName(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "hobby", Kind: "jira", Provider: stubProvider{}},
	}

	inst, err := ResolveByKind("jira", instances, "hobby")
	require.NoError(t, err)
	assert.Equal(t, "hobby", inst.Name)
}

func TestResolveByKind_nameNotFound(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
	}

	_, err := ResolveByKind("jira", instances, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker name not found for kind")
}

func TestResolve_byName(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "personal", Kind: "github", Provider: stubProvider{}},
	}

	inst, err := Resolve("personal", instances, "")
	require.NoError(t, err)
	assert.Equal(t, "personal", inst.Name)
	assert.Equal(t, "github", inst.Kind)
}

func TestResolve_unknownName(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
	}

	_, err := Resolve("nonexistent", instances, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker name not found")
}

func TestResolve_duplicateName(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "work", Kind: "github", Provider: stubProvider{}},
	}

	_, err := Resolve("work", instances, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous tracker name")
}

func TestResolve_autoDetectSingleKind(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "personal", Kind: "jira", Provider: stubProvider{}},
	}

	inst, err := Resolve("", instances, "")
	require.NoError(t, err)
	assert.Equal(t, "work", inst.Name)
}

func TestResolve_autoDetectMultipleKinds(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "personal", Kind: "github", Provider: stubProvider{}},
	}

	_, err := Resolve("", instances, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple tracker types configured")
}

func TestResolve_autoDetectNone(t *testing.T) {
	_, err := Resolve("", nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tracker configured")
}

// --- DetectKind tests ---

func TestDetectKind_githubIssue(t *testing.T) {
	assert.Equal(t, "github", DetectKind("octocat/hello-world#42"))
	assert.Equal(t, "github", DetectKind("org/repo#1"))
	assert.Equal(t, "github", DetectKind("my.org/my-repo#999"))
}

func TestDetectKind_githubRepo(t *testing.T) {
	assert.Equal(t, "github", DetectKind("octocat/hello-world"))
	assert.Equal(t, "github", DetectKind("org/repo"))
}

func TestDetectKind_jiraKey(t *testing.T) {
	assert.Equal(t, "", DetectKind("KAN-1"))
	assert.Equal(t, "", DetectKind("PROJ-123"))
}

func TestDetectKind_linearKey(t *testing.T) {
	assert.Equal(t, "", DetectKind("ENG-123"))
}

func TestDetectKind_empty(t *testing.T) {
	assert.Equal(t, "", DetectKind(""))
}

// --- Key-hint resolution tests ---

func TestResolve_keyHintSelectsGitHub(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "personal", Kind: "github", Provider: stubProvider{}},
	}

	inst, err := Resolve("", instances, "octocat/repo#1")
	require.NoError(t, err)
	assert.Equal(t, "personal", inst.Name)
	assert.Equal(t, "github", inst.Kind)
}

func TestResolve_keyHintGitHubRepoKey(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "oss", Kind: "github", Provider: stubProvider{}},
	}

	inst, err := Resolve("", instances, "octocat/repo")
	require.NoError(t, err)
	assert.Equal(t, "oss", inst.Name)
	assert.Equal(t, "github", inst.Kind)
}

func TestResolve_keyHintGitHubNoInstance(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
	}

	_, err := Resolve("", instances, "octocat/repo#1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tracker of detected kind configured")
}

func TestResolve_keyHintNonGitHubFallsBack(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "personal", Kind: "github", Provider: stubProvider{}},
	}

	// Jira-style key — DetectKind returns "", so falls back to multi-kind error
	_, err := Resolve("", instances, "KAN-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple tracker types configured")
}

func TestResolve_keyHintNonGitHubSingleKind(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "other", Kind: "jira", Provider: stubProvider{}},
	}

	// Jira-style key, single kind — auto-detect succeeds
	inst, err := Resolve("", instances, "KAN-1")
	require.NoError(t, err)
	assert.Equal(t, "work", inst.Name)
}

// --- DetectCandidateKinds tests ---

func TestDetectCandidateKinds(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want []string
	}{
		{name: "jira/linear key", key: "KAN-42", want: []string{"jira", "linear"}},
		{name: "another jira/linear", key: "PROJ-1", want: []string{"jira", "linear"}},
		{name: "github issue", key: "octocat/repo#42", want: []string{"github", "gitlab"}},
		{name: "github repo", key: "octocat/repo", want: []string{"github", "gitlab"}},
		{name: "azure devops", key: "Project/42", want: []string{"azuredevops"}},
		{name: "numeric shortcut", key: "123", want: []string{"shortcut"}},
		{name: "empty", key: "", want: nil},
		{name: "unknown", key: "!!!invalid!!!", want: nil},
		{name: "lowercase not jira", key: "kan-42", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectCandidateKinds(tt.key)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- ExtractProject tests ---

func TestExtractProject(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "jira key", key: "KAN-42", want: "KAN"},
		{name: "linear key", key: "ENG-123", want: "ENG"},
		{name: "github issue", key: "octocat/repo#42", want: "octocat/repo"},
		{name: "github repo", key: "octocat/repo", want: "octocat/repo"},
		{name: "azure devops", key: "Project/42", want: "Project"},
		{name: "shortcut numeric", key: "123", want: ""},
		{name: "unknown", key: "???", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractProject(tt.key))
		})
	}
}

// --- FindTracker tests ---

func TestFindTracker_singleConfigured(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
	}

	result, err := FindTracker(context.Background(), "KAN-42", instances)
	require.NoError(t, err)
	assert.Equal(t, "jira", result.Provider)
	assert.Equal(t, "KAN", result.Project)
	assert.Equal(t, "KAN-42", result.Key)
}

func TestFindTracker_ambiguousOneKind(t *testing.T) {
	// Both Jira instances configured — still only one kind, no probe needed.
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "other", Kind: "jira", Provider: stubProvider{}},
	}

	result, err := FindTracker(context.Background(), "KAN-42", instances)
	require.NoError(t, err)
	assert.Equal(t, "jira", result.Provider)
}

func TestFindTracker_ambiguousProbeSucceeds(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: fakeProvider{getIssueErr: fmt.Errorf("not found")}},
		{Name: "team", Kind: "linear", Provider: fakeProvider{}}, // succeeds
	}

	result, err := FindTracker(context.Background(), "KAN-42", instances)
	require.NoError(t, err)
	assert.Equal(t, "linear", result.Provider)
	assert.Equal(t, "KAN", result.Project)
}

func TestFindTracker_ambiguousProbeAllFail(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: fakeProvider{getIssueErr: fmt.Errorf("not found")}},
		{Name: "team", Kind: "linear", Provider: fakeProvider{getIssueErr: fmt.Errorf("not found")}},
	}

	_, err := FindTracker(context.Background(), "KAN-42", instances)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no configured tracker recognized the key")
}

func TestFindTracker_noMatch(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "github", Provider: stubProvider{}},
	}

	// KAN-42 → jira/linear candidates, but only github configured
	_, err := FindTracker(context.Background(), "KAN-42", instances)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no configured tracker matches key format")
}

func TestFindTracker_unrecognizedFormat(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
	}

	_, err := FindTracker(context.Background(), "!!!invalid", instances)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized key format")
}

// slowProvider blocks in GetIssue until the context is cancelled.
type slowProvider struct {
	stubProvider
}

func (slowProvider) GetIssue(ctx context.Context, _ string) (*Issue, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestFindTracker_probeTimesOut(t *testing.T) {
	instances := []Instance{
		{Name: "slow", Kind: "jira", Provider: slowProvider{}},
		{Name: "fast", Kind: "linear", Provider: fakeProvider{}}, // succeeds immediately
	}

	start := time.Now()
	result, err := FindTracker(context.Background(), "KAN-42", instances)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "linear", result.Provider)
	assert.Equal(t, "KAN", result.Project)
	// Should complete in ~probeTimeout (10s), not hang forever.
	// In practice it finishes in ~10s; give generous upper bound.
	assert.Less(t, elapsed, 15*time.Second, "should not hang forever")
}

// Nil and zero-length instance slices must surface a clear error
// instead of panicking or looping. This guards the "no trackers
// configured" code path that users hit on a fresh checkout.
func TestFindTracker_emptyInstances(t *testing.T) {
	_, err := FindTracker(context.Background(), "KAN-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no configured tracker matches key format")

	_, err = FindTracker(context.Background(), "KAN-1", []Instance{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no configured tracker matches key format")
}

// A pre-cancelled context must not cause FindTracker to hang —
// individual probe calls inherit the cancellation and return promptly,
// so the loop completes quickly. This documents current behavior
// (probeInstances does not short-circuit on ctx.Err, but providers
// that honour ctx still return fast).
func TestFindTracker_cancelledContext(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: slowProvider{}},
		{Name: "team", Kind: "linear", Provider: slowProvider{}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling

	start := time.Now()
	_, err := FindTracker(ctx, "KAN-42", instances)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 2*time.Second, "cancelled context should return promptly")
}

// ExtractProject edge cases that the existing TestExtractProject table
// does not exercise — bare separators, double-separator suffixes, and
// multi-# keys where the last # wins.
func TestExtractProject_edges(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"single slash with zero", "A/0", "A"},
		{"bare slash", "/", ""},
		{"double slash suffix", "x//", ""},
		{"multi hash", "owner/repo#1#2", "owner/repo#1"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractProject(tt.key))
		})
	}
}

func TestIssue_IsBug(t *testing.T) {
	tests := []struct {
		name string
		i    Issue
		want bool
	}{
		{"empty issue", Issue{}, false},
		{"type bug lowercase", Issue{Type: "bug"}, true},
		{"type Bug titlecase", Issue{Type: "Bug"}, true},
		{"type BUG uppercase", Issue{Type: "BUG"}, true},
		{"type feature", Issue{Type: "Feature"}, false},
		{"type empty", Issue{Type: ""}, false},
		{"label plain bug", Issue{Labels: []string{"bug"}}, true},
		{"label kind/bug", Issue{Labels: []string{"kind/bug"}}, true},
		{"label type:bug", Issue{Labels: []string{"type:bug"}}, true},
		{"label Bug among others, not first", Issue{Labels: []string{"priority/high", "Bug"}}, true},
		{"label bugfix is not a bug", Issue{Labels: []string{"bugfix"}}, false},
		{"label debug is not a bug", Issue{Labels: []string{"debug"}}, false},
		{"label kind/feature", Issue{Labels: []string{"kind/feature"}}, false},
		{"type wins even with non-bug labels", Issue{Type: "bug", Labels: []string{"priority/high"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.i.IsBug())
		})
	}
}
