package local

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/pipelinefsm"
	"github.com/gethuman-sh/human/internal/tracker"
)

func newStrictClient(t *testing.T) *Client {
	t.Helper()
	c, err := OpenMemory("LOC", "Ada", Strict())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func post(t *testing.T, c *Client, key, body string) {
	t.Helper()
	_, err := c.AddComment(context.Background(), key, body)
	require.NoError(t, err, body)
}

func TestMarkerIndex_recordsEveryMarkerInOrder(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	issue := create(t, c, "x")
	post(t, c, issue.Key, "just prose")
	post(t, c, issue.Key, "[human:planning-started]\ndaemon: d1\nbuild: abc")
	post(t, c, issue.Key, "[human:bug-verdict] confirmed\n\n## Explanation\nbody")

	markers, err := c.ListMarkers(ctx, issue.Key)
	require.NoError(t, err)
	require.Len(t, markers, 2, "prose is a comment, not a marker")
	assert.Equal(t, "planning-started", markers[0].Type)
	assert.Equal(t, "d1", markers[0].Fields["daemon"])
	assert.Equal(t, "Ada", markers[0].Author)
	assert.Equal(t, "bug-verdict", markers[1].Type)
	assert.Equal(t, "confirmed", markers[1].Head)
	assert.NotEmpty(t, markers[1].CommentID)

	comments, err := c.ListComments(ctx, issue.Key)
	require.NoError(t, err)
	assert.Len(t, comments, 3, "the comment thread is untouched: the index is derived, not a replacement")
}

func TestEvents_everyWriteLeavesOne_andVersionMoves(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	v0, err := c.Version(ctx)
	require.NoError(t, err)

	a := create(t, c, "a")
	b := create(t, c, "b")
	post(t, c, a.Key, "note")
	post(t, c, a.Key, "[human:plan]\n\n# Plan")
	require.NoError(t, c.LinkIssues(ctx, a.Key, b.Key, tracker.LinkBlocks))
	require.NoError(t, c.UnlinkIssues(ctx, a.Key, b.Key))
	require.NoError(t, c.TransitionIssue(ctx, a.Key, "Done"))
	require.NoError(t, c.AssignIssue(ctx, a.Key, "Ada"))
	title := "renamed"
	_, err = c.EditIssue(ctx, a.Key, tracker.EditOptions{Title: &title})
	require.NoError(t, err)

	events, err := c.ListEvents(ctx, a.Key)
	require.NoError(t, err)
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	assert.Equal(t, []string{"created", "comment", "marker", "linked", "unlinked", "status", "assigned", "edited"}, kinds)
	assert.Equal(t, "plan", events[2].Detail)
	assert.Equal(t, a.Key, events[2].Key)

	v1, err := c.Version(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, v0, v1)

	// A read changes nothing, so the version holds: that is what lets a poll
	// skip the listing.
	_, err = c.ListIssues(ctx, tracker.ListOptions{})
	require.NoError(t, err)
	v2, err := c.Version(ctx)
	require.NoError(t, err)
	assert.Equal(t, v1, v2)

	require.NoError(t, c.DeleteIssue(ctx, b.Key))
	v3, err := c.Version(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, v2, v3)
}

func TestFacets_kindAndMaturityFollowTheSharedConventions(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	idea, err := c.CreateIssue(ctx, &tracker.Issue{Title: "i", Labels: []string{tracker.IdeaLabel}})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"kind": "idea", "maturity": "idea"}, idea.Attributes)

	bug, err := c.CreateIssue(ctx, &tracker.Issue{Title: "b", Type: "Bug"})
	require.NoError(t, err)
	assert.Equal(t, "bug", bug.Attributes[tracker.AttrKind])
	assert.Equal(t, "pm", bug.Attributes[tracker.AttrMaturity])

	sec, err := c.CreateIssue(ctx, &tracker.Issue{Title: "s", Type: "Bug", Labels: []string{"security"}})
	require.NoError(t, err)
	assert.Equal(t, "security", sec.Attributes[tracker.AttrKind], "security wins over bug, as IsSecurity is disjoint from IsBug")

	feat, err := c.CreateIssue(ctx, &tracker.Issue{Title: "f", Type: "Feature"})
	require.NoError(t, err)
	assert.Equal(t, "feature", feat.Attributes[tracker.AttrKind])

	// Promotion strips the idea label; the kind and maturity follow.
	got, err := c.EditIssue(ctx, idea.Key, tracker.EditOptions{RemoveLabels: []string{tracker.IdeaLabel}})
	require.NoError(t, err)
	assert.Equal(t, "task", got.Attributes[tracker.AttrKind])
	assert.Equal(t, "pm", got.Attributes[tracker.AttrMaturity])

	// A plan attached is what makes a ticket planned.
	post(t, c, got.Key, "[human:plan]\n\n# Plan")
	got, err = c.GetIssue(ctx, got.Key)
	require.NoError(t, err)
	assert.Equal(t, "planned", got.Attributes[tracker.AttrMaturity])

	listed, err := c.ListIssues(ctx, tracker.ListOptions{})
	require.NoError(t, err)
	for _, is := range listed {
		assert.NotEmpty(t, is.Attributes[tracker.AttrKind], is.Key)
	}
}

func TestRecordPlacement_showsUpAsAttributes_andIsIdempotent(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	issue := create(t, c, "x")
	assert.Empty(t, issue.Attributes[tracker.AttrStage], "no placement until the daemon derived one")

	require.NoError(t, c.RecordPlacement(ctx, issue.Key, "planning", "running"))
	v1, _ := c.Version(ctx)
	require.NoError(t, c.RecordPlacement(ctx, issue.Key, "planning", "running"))
	v2, _ := c.Version(ctx)
	assert.Equal(t, v1, v2, "restating the same placement is not a change")

	got, err := c.GetIssue(ctx, issue.Key)
	require.NoError(t, err)
	assert.Equal(t, "planning", got.Attributes[tracker.AttrStage])
	assert.Equal(t, "running", got.Attributes[tracker.AttrState])
	assert.Equal(t, issue.UpdatedAt, got.UpdatedAt, "placement does not touch updated_at, so the board poll does not wake for its own write")

	require.NoError(t, c.RecordPlacement(ctx, issue.Key, "implementation", "running"))
	events, err := c.ListEvents(ctx, issue.Key)
	require.NoError(t, err)
	last := events[len(events)-1]
	assert.Equal(t, "placement", last.Kind)
	assert.Equal(t, "implementation/running", last.Detail)
	assert.Equal(t, "daemon", last.Actor)

	assert.Error(t, c.RecordPlacement(ctx, "LOC-99", "planning", "running"))
}

func TestStrict_refusesWhatTheGrammarRefuses(t *testing.T) {
	c := newStrictClient(t)
	ctx := context.Background()
	issue := create(t, c, "x")

	_, err := c.AddComment(ctx, issue.Key, "[human:ready-for-review]\n")
	assert.Error(t, err, "ready-for-review requires branch and commits")

	_, err = c.AddComment(ctx, issue.Key, "prose is never a marker and always fine")
	assert.NoError(t, err)

	lenient, _ := newTestClient(t)
	l := create(t, lenient, "y")
	_, err = lenient.AddComment(ctx, l.Key, "[human:ready-for-review]\n")
	assert.NoError(t, err, "lenient mode accepts what every other backend accepts")
}

func TestStrict_refusesARepeatedStageMarker(t *testing.T) {
	c := newStrictClient(t)
	ctx := context.Background()
	issue := create(t, c, "x")
	post(t, c, issue.Key, "[human:planning-started]")
	_, err := c.AddComment(ctx, issue.Key, "[human:planning-started]")
	assert.Error(t, err, "the same stage declared started twice in a row")

	// Something in between makes a second start a retry, which the machine allows.
	post(t, c, issue.Key, "[human:planning-failed]\nreason: died")
	post(t, c, issue.Key, "[human:planning-started]")
}

func TestStrict_refusesATransitionTheMachineDoesNotAllow(t *testing.T) {
	c := newStrictClient(t)
	ctx := context.Background()
	issue := create(t, c, "x")

	// A fresh ticket is `filed`; a deploy result before any deploy started is
	// a move the document has no edge for.
	_, err := c.AddComment(ctx, issue.Key, "[human:deployed]\npr: https://example/pr/1\nsha: abc")
	assert.Error(t, err)

	// The machine's own path is admitted step by step.
	post(t, c, issue.Key, "[human:planning-started]")
	post(t, c, issue.Key, "[human:claim]\ndaemon: d1")
	post(t, c, issue.Key, "[human:plan-ready]")

	markers, err := c.ListMarkers(ctx, issue.Key)
	require.NoError(t, err)
	assert.Len(t, markers, 3)
}

// A deploy declared failed while it was still running, then merging, is the
// sequence the machine had no edge for — so strict mode refused the merge's
// only record and the card was left Done over a failed-deploy trail (SC-5594).
func TestStrict_admitsAMergeRecordedAfterAFailedDeploy(t *testing.T) {
	c := newStrictClient(t)
	ctx := context.Background()
	issue := create(t, c, "x")

	post(t, c, issue.Key, "[human:deploy-started]")
	post(t, c, issue.Key, "[human:deploy-fix-started]\nbefore: deploy")
	post(t, c, issue.Key, "[human:deploy-failed]\nreason: declared failed while the fixer still ran")

	_, err := c.AddComment(ctx, issue.Key, "[human:deployed]\npr: https://example/pr/1")
	assert.NoError(t, err, "the merge must be recordable after the failure")
}

func TestStrict_admitsWhenTheHistoryAlreadyLeftTheMachine(t *testing.T) {
	lenient, _ := newTestClient(t)
	ctx := context.Background()
	issue := create(t, lenient, "x")
	post(t, lenient, issue.Key, "[human:deployed]\npr: https://example/pr/1\nsha: abc")

	// Flip the same store to strict: the thread is already off the machine, so
	// refusing now would strand it. The marker is admitted and logged.
	lenient.strict = true
	_, err := lenient.AddComment(ctx, issue.Key, "[human:planning-started]")
	assert.NoError(t, err)
}

func TestSeedMarkers_andSeedTrace(t *testing.T) {
	c := newStrictClient(t)
	ctx := context.Background()
	key, err := SeedMarkers(ctx, c, "seeded", []marker.Marker{
		{Type: "planning-started"},
		{Type: "plan-ready"},
	})
	require.NoError(t, err)
	markers, err := c.ListMarkers(ctx, key)
	require.NoError(t, err)
	assert.Len(t, markers, 2)

	// A corpus trace replays into a fresh ticket. The corpus has types only, so
	// the seed is admitted leniently; strictness is back for what follows.
	key2, err := SeedTrace(ctx, c, pipelinefsm.Trace{Key: "SC-1402", Markers: []string{
		"planning-started", "claim", "ticket-review-started", "planning-failed",
		"planning-started", "claim", "ticket-review-started", "ticket-review", "nothing-to-do",
	}})
	require.NoError(t, err)
	got, err := c.GetIssue(ctx, key2)
	require.NoError(t, err)
	assert.Equal(t, "SC-1402", got.Title, "the corpus key becomes the title; the store mints its own key")
	types, err := c.st.markerTypes(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, types, 9)
	assert.True(t, c.strict, "seeding does not leave the client lenient")
	_, err = c.AddComment(ctx, key2, "[human:deployed]\npr: x\nsha: y")
	assert.Error(t, err, "nothing-to-do is terminal; the machine refuses a deploy result there")

	_, err = SeedMarkers(ctx, c, "bad", []marker.Marker{{Type: "deployed"}})
	assert.Error(t, err, "a seed that the strict machine refuses fails loudly")
}

func TestMarkerIndex_survivesReopen(t *testing.T) {
	path := t.TempDir() + "/t.db"
	c, err := Open(path, "LOC", "Ada")
	require.NoError(t, err)
	issue := create(t, c, "x")
	post(t, c, issue.Key, "[human:planning-started]")
	require.NoError(t, c.RecordPlacement(context.Background(), issue.Key, "planning", "running"))
	require.NoError(t, c.Close())

	again, err := Open(path, "LOC", "Ada")
	require.NoError(t, err)
	defer func() { _ = again.Close() }()
	markers, err := again.ListMarkers(context.Background(), issue.Key)
	require.NoError(t, err)
	assert.Len(t, markers, 1)
	got, err := again.GetIssue(context.Background(), issue.Key)
	require.NoError(t, err)
	assert.Equal(t, "planning", got.Attributes[tracker.AttrStage])
	assert.False(t, markers[0].Created.IsZero())
	assert.WithinDuration(t, time.Now(), markers[0].Created, time.Minute)
}

// A database written by the build before the index existed is indexed on
// open: the markers already on the tickets become queryable, with their
// original author and time, and the classification follows.
func TestOpen_backfillsIndexFromOlderComments(t *testing.T) {
	path := t.TempDir() + "/old.db"
	c, err := Open(path, "LOC", "Ada")
	require.NoError(t, err)
	issue := create(t, c, "old")
	// Write a comment behind the index's back, as the earlier build did.
	_, err = c.st.db.Exec("INSERT INTO comments (issue, author, body, created_at) VALUES (1, 'Bob', ?, ?)",
		"[human:plan]\n\n# Plan", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(timeLayout))
	require.NoError(t, err)
	_, err = c.st.db.Exec("UPDATE issues SET kind = '' WHERE number = 1")
	require.NoError(t, err)
	require.NoError(t, c.Close())

	again, err := Open(path, "LOC", "Ada")
	require.NoError(t, err)
	defer func() { _ = again.Close() }()
	markers, err := again.ListMarkers(context.Background(), issue.Key)
	require.NoError(t, err)
	require.Len(t, markers, 1)
	assert.Equal(t, "plan", markers[0].Type)
	assert.Equal(t, "Bob", markers[0].Author)
	assert.Equal(t, 2026, markers[0].Created.Year())
	got, err := again.GetIssue(context.Background(), issue.Key)
	require.NoError(t, err)
	assert.Equal(t, "planned", got.Attributes[tracker.AttrMaturity])
	assert.Equal(t, "task", got.Attributes[tracker.AttrKind])
}
