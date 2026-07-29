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
