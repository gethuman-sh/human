package cmddaemon

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

// stubProvider implements tracker.Provider; only ListComments returns data, the
// rest satisfy the interface so the stub can sit in tracker.Instance.Provider.
type stubProvider struct {
	comments []tracker.Comment
	err      error // when non-nil, ListComments fails — simulates a fetch timeout/blip
}

func (s *stubProvider) ListIssues(context.Context, tracker.ListOptions) ([]tracker.Issue, error) {
	return nil, nil
}
func (s *stubProvider) GetIssue(context.Context, string) (*tracker.Issue, error) { return nil, nil }
func (s *stubProvider) CreateIssue(context.Context, *tracker.Issue) (*tracker.Issue, error) {
	return nil, nil
}
func (s *stubProvider) ListComments(context.Context, string) ([]tracker.Comment, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.comments, nil
}
func (s *stubProvider) AddComment(context.Context, string, string) (*tracker.Comment, error) {
	return nil, nil
}
func (s *stubProvider) LinkIssues(context.Context, string, string, tracker.LinkKind) error {
	return nil
}
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

func TestScanReadyForReview_BoardCards(t *testing.T) {
	t0 := time.Unix(1000, 0)
	prov := &stubProvider{comments: []tracker.Comment{
		{Body: "[human:plan-ready]\nengineering: HUM-7", Created: t0},
	}}
	jobs := []fetchJob{{inst: tracker.Instance{Name: "work", Kind: "shortcut", Provider: prov}}}
	results := []daemon.TrackerIssuesResult{{
		TrackerRole: "pm",
		Issues:      []tracker.Issue{{Key: "SC-1", StatusType: tracker.CategoryUnstarted}},
	}}

	_, _, cards := scanReadyForReview(jobs, results, zerolog.Nop(), nil)
	require.Contains(t, cards, "SC-1")
	assert.Equal(t, daemon.BoardPlanning, cards["SC-1"].Stage)
	assert.Equal(t, daemon.BoardDone, cards["SC-1"].State)
	assert.Equal(t, "HUM-7", cards["SC-1"].EngineeringKey)
}

// A transient ListComments failure on an open, in-flight PM ticket must not
// silently drop the key (which downstream renders as an actionable Backlog
// card indistinguishable from never-worked). It must emit an explicit
// degraded card AND log the failure with the key and cause (ticket 1700).
func TestScanReadyForReview_FetchError_EmitsDegradedCardAndLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	prov := &stubProvider{err: errors.New("comment fetch timeout")}
	jobs := []fetchJob{{inst: tracker.Instance{Name: "work", Kind: "shortcut", Provider: prov}}}
	results := []daemon.TrackerIssuesResult{{
		TrackerRole: "pm",
		Issues:      []tracker.Issue{{Key: "SC-1", StatusType: tracker.CategoryStarted}},
	}}

	_, _, cards := scanReadyForReview(jobs, results, logger, nil)

	require.Contains(t, cards, "SC-1", "an erroring fetch must still yield a card, not a silent drop")
	assert.True(t, cards["SC-1"].Degraded, "the card must be flagged degraded")
	assert.NotEqual(t, daemon.BoardBacklog, cards["SC-1"].Stage,
		"a degraded card with no last-known stage must not masquerade as an actionable Backlog card")
	assert.Contains(t, buf.String(), "SC-1", "the failure must be logged with the key")
	assert.Contains(t, buf.String(), "comment fetch timeout", "the log must carry the cause")
}

// With a last-known stage available, a fetch error carries that stage forward
// (degraded) rather than demoting the card (ticket 1700).
func TestScanReadyForReview_FetchError_PrefersLastKnownStage(t *testing.T) {
	logger := zerolog.Nop()
	prov := &stubProvider{err: errors.New("blip")}
	jobs := []fetchJob{{inst: tracker.Instance{Name: "work", Kind: "shortcut", Provider: prov}}}
	results := []daemon.TrackerIssuesResult{{
		TrackerRole: "pm",
		Issues:      []tracker.Issue{{Key: "SC-1", StatusType: tracker.CategoryStarted}},
	}}
	prev := func(key string) (daemon.BoardCard, bool) {
		return daemon.BoardCard{Stage: daemon.BoardImplementation, State: daemon.BoardRunning}, true
	}

	_, _, cards := scanReadyForReview(jobs, results, logger, prev)

	assert.True(t, cards["SC-1"].Degraded)
	assert.Equal(t, daemon.BoardImplementation, cards["SC-1"].Stage, "the last-known stage is preserved")
	assert.Equal(t, daemon.BoardRunning, cards["SC-1"].State)
}

// SC-2307 AC3: a tracker-read failure must never fabricate "no plan exists". The
// degraded card carries the last-known HasPlan forward so an unreachable tracker
// never invites re-planning a ticket a human already planned.
func TestScanReadyForReview_FetchError_PreservesHasPlan(t *testing.T) {
	logger := zerolog.Nop()
	prov := &stubProvider{err: errors.New("tracker unreachable")}
	jobs := []fetchJob{{inst: tracker.Instance{Name: "work", Kind: "shortcut", Provider: prov}}}
	results := []daemon.TrackerIssuesResult{{
		TrackerRole: "pm",
		Issues:      []tracker.Issue{{Key: "SC-1", StatusType: tracker.CategoryStarted}},
	}}
	prev := func(key string) (daemon.BoardCard, bool) {
		return daemon.BoardCard{Stage: daemon.BoardImplementation, State: daemon.BoardRunning, HasPlan: true}, true
	}

	_, _, cards := scanReadyForReview(jobs, results, logger, prev)

	assert.True(t, cards["SC-1"].Degraded, "an unreadable tracker yields a degraded, non-launchable card")
	assert.True(t, cards["SC-1"].HasPlan,
		"a read failure must never read as 'no plan exists' — the last-known plan presence is preserved")
}

// The lite fetcher must return without error (and without running the comment
// scan) even when there is nothing to fetch, so the board's fast path degrades
// to an empty board rather than a failure.
func TestFetchTrackerIssuesLiteFunc_EmptyRegistry(t *testing.T) {
	reg, err := daemon.NewProjectRegistry(nil)
	require.NoError(t, err)

	results, err := fetchTrackerIssuesLiteFunc(reg, vault.NewResolver())()
	require.NoError(t, err)
	assert.Empty(t, results)
}

func (s *stubProvider) UnlinkIssues(context.Context, string, string) error {
	return nil
}

// projectsOf names the (instance, project) pairs a listing would fetch, so a
// test can assert what the daemon asks for rather than how it asks.
func projectsOf(jobs []fetchJob) []string {
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.inst.Kind+":"+j.project)
	}
	return out
}

// A GitHub entry is a tracker and is listed like one. It took SC-1671, SC-2132
// and SC-3868 to decide when it was not — every one of those a symptom of the
// section holding something that was not a tracker at all. The forge lives in
// its own section now, so this list is trackers and the question is gone
// ([SC-3876]).
func TestListingJobs_ListsEveryConfiguredTracker(t *testing.T) {
	jobs := listingJobs([]tracker.Instance{
		{Name: "human", Kind: "github"},
		{Name: "human", Kind: "shortcut"},
	}, "/proj")

	assert.Equal(t, []string{"github:", "shortcut:"}, projectsOf(jobs))
}

// A team whose tracker IS GitHub declares role: pm, and must be listed exactly
// as before — the rule is about a declared role, not about the vendor.
func TestListingJobs_KeepsADeclaredGitHubTracker(t *testing.T) {
	jobs := listingJobs([]tracker.Instance{
		{Name: "work", Kind: "github", Role: "pm", Projects: []string{"acme/web"}},
	}, "/proj")

	assert.Equal(t, []string{"github:acme/web"}, projectsOf(jobs))
}

// Only Shortcut infers a role for free, so a Linear or Jira tracker configured
// without one is the ordinary case, not a forge in disguise. Skipping it would
// empty the board of the very work it exists to show.
func TestListingJobs_KeepsARolelessNonForgeTracker(t *testing.T) {
	jobs := listingJobs([]tracker.Instance{
		{Name: "work", Kind: "linear"},
		{Name: "ops", Kind: "jira"},
	}, "/proj")

	assert.Equal(t, []string{"linear:", "jira:"}, projectsOf(jobs))
}

// One job per configured project, each carrying the project dir so a later
// comment scan can route the ticket back to the project that owns it.
func TestListingJobs_OneJobPerProjectCarryingTheDir(t *testing.T) {
	jobs := listingJobs([]tracker.Instance{
		{Name: "work", Kind: "shortcut", Projects: []string{"team-a", "team-b"}},
	}, "/proj")

	assert.Equal(t, []string{"shortcut:team-a", "shortcut:team-b"}, projectsOf(jobs))
	for _, j := range jobs {
		assert.Equal(t, "/proj", j.dir)
	}
}

// The board's listing declares itself a poll loop, so a backend can refuse work
// it cannot answer cheaply. GitHub refuses an unscoped listing, and the refusal
// arrives as this result's Err — which reaches the board's error banner, in the
// place the missing tickets would have been ([SC-3888]).
func TestListTrackerIssues_boardListingIsUnattended(t *testing.T) {
	var got tracker.ListOptions
	prov := &optionsCapturingProvider{seen: &got}

	page, err := tracker.ListIssuesPage(context.Background(), prov, tracker.ListOptions{
		MaxResults: 200,
		Unattended: true,
	})
	require.NoError(t, err)
	assert.Empty(t, page.Issues)
	assert.True(t, got.Unattended,
		"a listing that does not say it is a loop cannot be protected from being one")
}

// optionsCapturingProvider records the ListOptions it was handed.
type optionsCapturingProvider struct {
	stubProvider
	seen *tracker.ListOptions
}

func (p *optionsCapturingProvider) ListIssues(_ context.Context, opts tracker.ListOptions) ([]tracker.Issue, error) {
	*p.seen = opts
	return nil, nil
}
