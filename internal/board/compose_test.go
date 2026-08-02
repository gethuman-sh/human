package board

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
)

// pmResult builds a PM-role result carrying the given issues and derived cards.
func pmResult(issues []tracker.Issue, cards map[string]daemon.BoardCard) daemon.TrackerIssuesResult {
	return daemon.TrackerIssuesResult{
		TrackerName: "human",
		TrackerKind: "shortcut",
		TrackerRole: "pm",
		Project:     "board",
		Issues:      issues,
		BoardCards:  cards,
	}
}

// cardByKey finds a composed card.
func cardByKey(t *testing.T, view daemon.BoardView, key string) daemon.BoardViewCard {
	t.Helper()
	for _, c := range view.Cards {
		if c.Key == key {
			return c
		}
	}
	require.FailNowf(t, "card not found", "expected %s in the composed view", key)
	return daemon.BoardViewCard{}
}

// The whole point of the split: Compose produces what is true of the PROJECT and
// must leave the viewer's own fields untouched, so the same composition can be
// computed once (on the daemon) and rendered by anyone.
func TestCompose_LeavesViewerOwnedFieldsZero(t *testing.T) {
	view := Compose([]daemon.TrackerIssuesResult{pmResult(
		[]tracker.Issue{{Key: "SC-1", Title: "one"}},
		map[string]daemon.BoardCard{"SC-1": {Stage: daemon.BoardIdeas}},
	)}, true)

	c := cardByKey(t, view, "SC-1")
	assert.Zero(t, c.IdeaColumn, "idea column is a viewer preference")
	assert.False(t, c.Hidden, "hiding is a viewer filter")
	assert.Empty(t, c.MockupSlug)
	assert.Empty(t, c.MockupState)
	assert.Empty(t, c.MockupChosenSlug)
	assert.Empty(t, c.MockupChosenFile)
	assert.Empty(t, view.ColumnOrder, "hand-sorted order is a viewer preference")
}

// The completed-record flag rides from the derived card onto the wire card so
// the Bugs pane can suppress the on-demand "Find related work" action (SC-2405).
func TestCompose_HasRelatedRecordCopied(t *testing.T) {
	view := Compose([]daemon.TrackerIssuesResult{pmResult(
		[]tracker.Issue{{Key: "SC-1", Title: "with record"}, {Key: "SC-2", Title: "without record"}},
		map[string]daemon.BoardCard{
			"SC-1": {Stage: daemon.BoardBacklog, HasRelatedRecord: true},
			"SC-2": {Stage: daemon.BoardBacklog},
		},
	)}, true)

	assert.True(t, cardByKey(t, view, "SC-1").HasRelatedRecord)
	assert.False(t, cardByKey(t, view, "SC-2").HasRelatedRecord)
}

// The pre-planning stop decision rides from the derived card onto the wire card
// so the desktop can render the "decided" badge and Decision section (SC-2699).
func TestCompose_carriesStopDecision(t *testing.T) {
	view := Compose([]daemon.TrackerIssuesResult{pmResult(
		[]tracker.Issue{{Key: "SC-1", Title: "decided"}},
		map[string]daemon.BoardCard{
			"SC-1": {
				Stage:         daemon.BoardBacklog,
				State:         daemon.BoardDone,
				StopDecision:  "superseded",
				StopLinkedKey: "SC-100",
				StopReasoning: "Same surface as SC-100, which carries the work",
			},
		},
	)}, true)

	c := cardByKey(t, view, "SC-1")
	assert.Equal(t, "superseded", c.StopDecision)
	assert.Equal(t, "SC-100", c.StopLinkedKey)
	assert.Equal(t, "Same surface as SC-100, which carries the work", c.StopReasoning)
}

// Hidden cards must still be composed: the frontend filters them, so dropping
// them here would make "reveal hidden" impossible without a refetch.
func TestCompose_ReturnsCardsTheViewerMayHide(t *testing.T) {
	view := Compose([]daemon.TrackerIssuesResult{pmResult(
		[]tracker.Issue{{Key: "SC-1", Title: "one"}, {Key: "SC-2", Title: "two"}},
		map[string]daemon.BoardCard{
			"SC-1": {Stage: daemon.BoardBacklog},
			"SC-2": {Stage: daemon.BoardBacklog},
		},
	)}, false)

	assert.Len(t, view.Cards, 2, "Compose knows nothing about which cards a viewer hides")
}

// A card whose markers could not be read is pinned to its last-known column
// rather than presenting as idle, actionable Backlog work (1700).
func TestCompose_DegradedCardKeepsItsLastKnownStage(t *testing.T) {
	view := Compose([]daemon.TrackerIssuesResult{pmResult(
		[]tracker.Issue{{Key: "SC-1", Title: "one"}},
		map[string]daemon.BoardCard{"SC-1": {Stage: daemon.BoardImplementation, State: daemon.BoardRunning, Degraded: true}},
	)}, false)

	c := cardByKey(t, view, "SC-1")
	assert.Equal(t, string(daemon.BoardImplementation), c.Stage)
	assert.True(t, c.Degraded)
}

// A degraded card with no prior stage falls back to Backlog rather than to the
// hidden lane, so it stays visible while unreadable.
func TestCompose_DegradedCardWithoutAStageFallsBackToBacklog(t *testing.T) {
	for _, stage := range []daemon.BoardStage{"", daemon.BoardHidden} {
		view := Compose([]daemon.TrackerIssuesResult{pmResult(
			[]tracker.Issue{{Key: "SC-1", Title: "one"}},
			map[string]daemon.BoardCard{"SC-1": {Stage: stage, Degraded: true}},
		)}, false)

		c := cardByKey(t, view, "SC-1")
		assert.Equal(t, string(daemon.BoardBacklog), c.Stage, "degraded stage %q", stage)
	}
}

// A ticket that never entered the pipeline is placed by its own nature: closed
// work is not shown, an idea sits in Ideas by its label, everything else lands
// in Backlog.
func TestCompose_UnstartedTicketsArePlacedByNature(t *testing.T) {
	issues := []tracker.Issue{
		{Key: "SC-done", Title: "shipped", StatusType: tracker.CategoryDone},
		{Key: "SC-closed", Title: "dropped", StatusType: tracker.CategoryClosed},
		{Key: "SC-idea", Title: "a thought", Labels: []string{"human/idea"}},
		{Key: "SC-plain", Title: "work"},
	}
	view := Compose([]daemon.TrackerIssuesResult{pmResult(issues, map[string]daemon.BoardCard{})}, false)

	keys := make(map[string]string, len(view.Cards))
	for _, c := range view.Cards {
		keys[c.Key] = c.Stage
	}
	assert.NotContains(t, keys, "SC-done", "closed work that never started is not board work")
	assert.NotContains(t, keys, "SC-closed")
	assert.Equal(t, string(daemon.BoardIdeas), keys["SC-idea"])
	assert.Equal(t, string(daemon.BoardBacklog), keys["SC-plain"])
}

// A derived hidden stage is a closed ticket that never entered the pipeline.
func TestCompose_HiddenStageIsNotRendered(t *testing.T) {
	view := Compose([]daemon.TrackerIssuesResult{pmResult(
		[]tracker.Issue{{Key: "SC-1", Title: "one"}},
		map[string]daemon.BoardCard{"SC-1": {Stage: daemon.BoardHidden}},
	)}, false)

	assert.Empty(t, view.Cards)
}

// With no PM-role tracker the board must say so rather than render five empty
// columns that read as "no work" (SC-1655).
func TestCompose_NoPMTrackerExplainsItself(t *testing.T) {
	view := Compose([]daemon.TrackerIssuesResult{{
		TrackerName: "human", TrackerKind: "linear", Project: "eng",
	}}, false)

	assert.Empty(t, view.Cards)
	assert.NotEmpty(t, view.Notice, "an empty board must explain why it is empty")
}

// A fetch error on the PM result rides along with whatever did come back, so the
// frontend can show a banner instead of a silently short board.
func TestCompose_PMFetchErrorIsCarried(t *testing.T) {
	res := pmResult([]tracker.Issue{{Key: "SC-1", Title: "one"}}, map[string]daemon.BoardCard{"SC-1": {Stage: daemon.BoardBacklog}})
	res.Err = "tracker unreachable"

	view := Compose([]daemon.TrackerIssuesResult{res}, false)

	assert.Equal(t, "tracker unreachable", view.Error)
	assert.Len(t, view.Cards, 1, "the error must not discard the cards that did arrive")
}

// Facts about the ticket and its derived run must survive composition intact —
// this is the payload the whole board renders from.
func TestCompose_CarriesTicketAndRunFacts(t *testing.T) {
	entered := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	view := Compose([]daemon.TrackerIssuesResult{pmResult(
		[]tracker.Issue{{
			Key: "SC-1", Title: "one", URL: "https://example/1",
			Description: "body", Assignee: "stephan", Labels: []string{"bug"},
		}},
		map[string]daemon.BoardCard{"SC-1": {
			Stage: daemon.BoardImplementation, State: daemon.BoardRunning,
			Branch: "autofix/sc-1", PRURL: "https://example/pr/1",
			EngineeringKey: "HUM-1", Verdict: "pass", DeployPhase: "pr-review",
			StageEnteredAt: entered,
			Options:        []daemon.BoardOption{{ID: "1", Label: "A"}}, OptionsContext: "why",
		}},
	)}, true)

	c := cardByKey(t, view, "SC-1")
	assert.Equal(t, "one", c.Title)
	assert.Equal(t, "https://example/1", c.URL)
	assert.Equal(t, "body", c.Description)
	assert.Equal(t, "stephan", c.Assignee)
	assert.Equal(t, "autofix/sc-1", c.Branch)
	assert.Equal(t, "https://example/pr/1", c.PRURL)
	assert.Equal(t, "HUM-1", c.EngineeringKey)
	assert.Equal(t, "pass", c.Verdict)
	assert.Equal(t, "pr-review", c.DeployPhase)
	assert.Equal(t, entered.Format(time.RFC3339), c.StageEnteredAt)
	assert.Equal(t, "human", c.Tracker)
	assert.Equal(t, "shortcut", c.TrackerKind)
	assert.True(t, c.Bug, "a bug label must reach the Bugs pane")
	require.Len(t, c.Options, 1)
	assert.Equal(t, "why", c.OptionsContext)
	assert.True(t, view.DockerAvailable, "docker availability is the launching host's, passed in")
}

// blockedIssue builds an issue waiting for the given keys.
func blockedIssue(key, title string, blockers ...string) tracker.Issue {
	issue := tracker.Issue{Key: key, Title: title}
	for _, b := range blockers {
		issue.Links = append(issue.Links, tracker.IssueLink{Key: b, Kind: tracker.LinkBlocks, Inbound: true})
	}
	return issue
}

// A card that will not start has to say why on its face, or it reads as idle
// work nobody picked up — the reading the board must never give.
func TestCompose_CardNamesTheWorkItWaitsFor(t *testing.T) {
	view := Compose([]daemon.TrackerIssuesResult{pmResult(
		[]tracker.Issue{
			blockedIssue("SC-2", "waits", "SC-1"),
			{Key: "SC-1", Title: "goes first"},
		},
		map[string]daemon.BoardCard{},
	)}, true)

	assert.Equal(t, []string{"SC-1"}, cardByKey(t, view, "SC-2").Blockers)
	assert.Empty(t, cardByKey(t, view, "SC-1").Blockers, "the blocker itself waits for nothing")
}

// A blocker that finished has left the board, and its absence is what tells us
// so — no second fetch, no stale badge on work that is free to start.
func TestCompose_FinishedBlockerLeavesNoBadge(t *testing.T) {
	view := Compose([]daemon.TrackerIssuesResult{pmResult(
		[]tracker.Issue{
			blockedIssue("SC-2", "waits", "SC-1"),
			{Key: "SC-1", Title: "shipped", StatusType: tracker.CategoryDone},
		},
		map[string]daemon.BoardCard{},
	)}, true)

	assert.Empty(t, cardByKey(t, view, "SC-2").Blockers)
}

// An association is not an ordering, and blocking something else is not waiting
// for it.
func TestCompose_OnlyWaitingRelationsBadge(t *testing.T) {
	issue := tracker.Issue{Key: "SC-2", Title: "two", Links: []tracker.IssueLink{
		{Key: "SC-1", Kind: tracker.LinkBlocks, Inbound: false},
		{Key: "SC-1", Kind: tracker.LinkRelated, Inbound: true},
	}}
	view := Compose([]daemon.TrackerIssuesResult{pmResult(
		[]tracker.Issue{issue, {Key: "SC-1", Title: "one"}},
		map[string]daemon.BoardCard{},
	)}, true)

	assert.Empty(t, cardByKey(t, view, "SC-2").Blockers)
}
