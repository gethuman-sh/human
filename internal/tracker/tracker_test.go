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
func (stubProvider) LinkIssues(context.Context, string, string) error               { return nil }
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

// SC-nnn is unambiguously Shortcut's own display format, so DetectKind must
// name it rather than falling through to "".
func TestDetectKind_shortcutDisplayKey(t *testing.T) {
	assert.Equal(t, "shortcut", DetectKind("SC-879"))
	assert.Equal(t, "shortcut", DetectKind("sc-1"))
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

func TestResolve_keyHintGitLabOnlyResolves(t *testing.T) {
	// "namespace/project#IID" is valid for both GitHub and GitLab; a
	// gitlab-only config must still resolve it rather than failing because the
	// shape was guessed as github.
	instances := []Instance{
		{Name: "work", Kind: "gitlab", Provider: stubProvider{}},
	}

	inst, err := Resolve("", instances, "group/proj#7")
	require.NoError(t, err)
	assert.Equal(t, "work", inst.Name)
	assert.Equal(t, "gitlab", inst.Kind)
}

func TestResolve_keyHintGitHubGitLabAmbiguous(t *testing.T) {
	// With both kinds configured the key is genuinely ambiguous, so the
	// resolver must ask the user to disambiguate rather than silently guessing.
	instances := []Instance{
		{Name: "gh", Kind: "github", Provider: stubProvider{}},
		{Name: "gl", Kind: "gitlab", Provider: stubProvider{}},
	}

	_, err := Resolve("", instances, "group/proj#7")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple tracker types match the key")
}

func TestResolve_keyHintNarrowsToCandidateKind(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: stubProvider{}},
		{Name: "personal", Kind: "github", Provider: stubProvider{}},
	}

	// "KAN-1" is a jira/linear shape; github is not a candidate, so even with
	// multiple kinds configured the resolver narrows to the sole matching kind.
	inst, err := Resolve("", instances, "KAN-1")
	require.NoError(t, err)
	assert.Equal(t, "work", inst.Name)
	assert.Equal(t, "jira", inst.Kind)
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
		// SC-nnn is the form the tool prints everywhere; its shape also matches
		// the jira/linear regex, so shortcut is added as an extra candidate and
		// the FindTracker probe disambiguates.
		{name: "shortcut display key", key: "SC-879", want: []string{"jira", "linear", "shortcut"}},
		// Lowercase "sc-157" does not match the uppercase-only jira/linear regex,
		// so it resolves unambiguously to shortcut alone.
		{name: "shortcut display key lowercase", key: "sc-157", want: []string{"shortcut"}},
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
		// SC-nnn carries no project prefix — it must not yield "SC" the way a
		// jira/linear key yields its project, or the resolved key would be wrong.
		{name: "shortcut display key", key: "SC-879", want: ""},
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

// A key in the tool's own SC-nnn display form must resolve to the configured
// Shortcut workspace: the shape is ambiguous (jira/linear/shortcut), Jira's
// probe fails, and Shortcut's succeeds — so FindTracker lands on shortcut with
// no project prefix and the key kept verbatim.
func TestFindTracker_shortcutDisplayKey(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "jira", Provider: fakeProvider{getIssueErr: fmt.Errorf("not found")}},
		{Name: "board", Kind: "shortcut", Provider: fakeProvider{}}, // succeeds
	}

	result, err := FindTracker(context.Background(), "SC-879", instances)
	require.NoError(t, err)
	assert.Equal(t, "shortcut", result.Provider)
	assert.Equal(t, "", result.Project)
	assert.Equal(t, "SC-879", result.Key)
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

func TestIsBugType(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want bool
	}{
		{"bug lowercase", "bug", true},
		{"Bug titlecase", "Bug", true},
		{"type:bug", "type:bug", true},
		{"kind/bug", "kind/bug", true},
		{"empty", "", false},
		{"Task", "Task", false},
		// Token matching, not substring matching — "debug" and "bugfix"
		// contain "bug" but must not classify as the defect type.
		{"debug is not a bug", "debug", false},
		{"bugfix is not a bug", "bugfix", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsBugType(tt.s))
		})
	}
}

func TestCreateLabels(t *testing.T) {
	tests := []struct {
		name string
		i    Issue
		want []string
	}{
		{"bug type without labels", Issue{Type: "bug"}, []string{"bug"}},
		{"bug type titlecase without labels", Issue{Type: "Bug"}, []string{"bug"}},
		{"bug type appends to existing labels", Issue{Type: "bug", Labels: []string{"urgent"}}, []string{"urgent", "bug"}},
		// An already-present bug token means the defect marking is intact —
		// appending again would create a duplicate label on the tracker.
		{"bug type with bug label", Issue{Type: "bug", Labels: []string{"bug"}}, []string{"bug"}},
		{"bug type with kind/bug label", Issue{Type: "Bug", Labels: []string{"kind/bug"}}, []string{"kind/bug"}},
		{"non-bug type passes labels through", Issue{Type: "Feature", Labels: []string{"urgent"}}, []string{"urgent"}},
		{"empty type passes nil through", Issue{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CreateLabels(&tt.i))
		})
	}
}

func TestCreateLabels_doesNotMutateInput(t *testing.T) {
	// The caller's Issue is shared state (it is re-used for the tracker
	// response) — appending the bug label in place would leak the synthetic
	// label back into the caller's slice.
	labels := []string{"urgent"}
	i := Issue{Type: "bug", Labels: labels}

	got := CreateLabels(&i)

	assert.Equal(t, []string{"urgent", "bug"}, got)
	assert.Equal(t, []string{"urgent"}, labels)
	assert.Equal(t, []string{"urgent"}, i.Labels)
}

func TestIssue_IsIdea(t *testing.T) {
	tests := []struct {
		name string
		i    Issue
		want bool
	}{
		{"empty issue", Issue{}, false},
		{"label idea lowercase", Issue{Labels: []string{"idea"}}, true},
		{"label Idea titlecase", Issue{Labels: []string{"Idea"}}, true},
		{"label human/idea", Issue{Labels: []string{"human/idea"}}, true},
		{"canonical IdeaLabel constant", Issue{Labels: []string{IdeaLabel}}, true},
		{"label kind/idea", Issue{Labels: []string{"kind/idea"}}, true},
		{"label type:idea", Issue{Labels: []string{"type:idea"}}, true},
		{"type idea", Issue{Type: "idea"}, true},
		{"type Idea", Issue{Type: "Idea"}, true},
		{"type human/idea", Issue{Type: "human/idea"}, true},
		{"type kind/idea", Issue{Type: "kind/idea"}, true},
		{"type type:idea", Issue{Type: "type:idea"}, true},
		{"label ideation is not an idea", Issue{Labels: []string{"ideation"}}, false},
		// hasToken splits on '/' and ':' only, so "no-idea" stays one segment
		// ("no-idea" != "idea") — hyphenated non-matches must not classify.
		{"label no-idea is not an idea", Issue{Labels: []string{"no-idea"}}, false},
		{"label bugidea is not an idea", Issue{Labels: []string{"bugidea"}}, false},
		{"type ideation is not an idea", Issue{Type: "ideation"}, false},
		{"idea among other labels", Issue{Labels: []string{"priority/high", "human/idea"}}, true},
		{"non-idea labels", Issue{Labels: []string{"bug", "kind/feature"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.i.IsIdea())
		})
	}
}

// fakeLabelEditor is executable documentation of the EditOptions label
// contract every provider must honour: labels are a set — AddLabels is
// idempotent (adding a present label changes nothing), RemoveLabels ignores
// absent labels (a remove-absent is a no-op, not an error), and one call may
// combine both to swap labels atomically.
type fakeLabelEditor struct {
	labels []string
}

var _ Editor = (*fakeLabelEditor)(nil)

func (f *fakeLabelEditor) EditIssue(_ context.Context, _ string, opts EditOptions) (*Issue, error) {
	removed := make(map[string]bool, len(opts.RemoveLabels))
	for _, l := range opts.RemoveLabels {
		removed[l] = true
	}

	seen := make(map[string]bool, len(f.labels)+len(opts.AddLabels))
	next := make([]string, 0, len(f.labels)+len(opts.AddLabels))
	for _, l := range f.labels {
		if removed[l] || seen[l] {
			continue
		}
		seen[l] = true
		next = append(next, l)
	}
	for _, l := range opts.AddLabels {
		if seen[l] {
			continue
		}
		seen[l] = true
		next = append(next, l)
	}

	f.labels = next
	return &Issue{Labels: next}, nil
}

func TestEditOptions_labelContract(t *testing.T) {
	tests := []struct {
		name    string
		initial []string
		opts    EditOptions
		want    []string
	}{
		{"add to empty", nil, EditOptions{AddLabels: []string{IdeaLabel}}, []string{IdeaLabel}},
		{"add is idempotent", []string{"bug"}, EditOptions{AddLabels: []string{"bug"}}, []string{"bug"}},
		{"remove absent is a no-op", []string{"bug"}, EditOptions{RemoveLabels: []string{"nope"}}, []string{"bug"}},
		{"remove present", []string{"bug", "keep"}, EditOptions{RemoveLabels: []string{"bug"}}, []string{"keep"}},
		{"swap in one call", []string{IdeaLabel, "keep"},
			EditOptions{AddLabels: []string{"human/ready"}, RemoveLabels: []string{IdeaLabel}},
			[]string{"keep", "human/ready"}},
		{"no label opts leaves labels untouched", []string{"bug"}, EditOptions{}, []string{"bug"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ed := &fakeLabelEditor{labels: tt.initial}
			issue, err := ed.EditIssue(context.Background(), "KEY-1", tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want, issue.Labels)
		})
	}
}

// TestInferRole_SC254 locks in that engineering topology is opt-in: a Linear
// instance with no explicit role must NOT infer "engineering" (that inference
// silently minted unwanted engineering tickets in single-tracker setups), while
// an explicitly configured engineering role is still honored. The pm inference
// for Shortcut deliberately stays (read-side board scanning relies on it).
func TestInferRole_SC254(t *testing.T) {
	tests := []struct {
		name string
		inst Instance
		want string
	}{
		{"linear without role no longer infers engineering", Instance{Kind: "linear"}, ""},
		{"explicit engineering role honored", Instance{Kind: "linear", Role: "engineering"}, "engineering"},
		{"explicit engineering role honored on any kind", Instance{Kind: "github", Role: "engineering"}, "engineering"},
		{"shortcut still infers pm", Instance{Kind: "shortcut"}, "pm"},
		{"explicit role overrides kind", Instance{Kind: "shortcut", Role: "engineering"}, "engineering"},
		{"unknown kind without role is empty", Instance{Kind: "jira"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.inst.InferRole())
		})
	}
}
