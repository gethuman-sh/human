package local

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func newTestClient(t *testing.T) (*Client, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	c, err := Open(":memory:", "LOC", "Ada", WithClock(func() time.Time { return now }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c, &now
}

func create(t *testing.T, c *Client, title string) *tracker.Issue {
	t.Helper()
	issue, err := c.CreateIssue(context.Background(), &tracker.Issue{Title: title})
	require.NoError(t, err)
	return issue
}

func TestValidatePrefix(t *testing.T) {
	assert.NoError(t, ValidatePrefix("LOC"))
	assert.NoError(t, ValidatePrefix("A1"))
	assert.Error(t, ValidatePrefix("loc"), "lowercase would never match the key grammar")
	assert.Error(t, ValidatePrefix("L"), "a single letter is not a project in the key grammar")
	assert.Error(t, ValidatePrefix("1A"))
	assert.Error(t, ValidatePrefix("SC"), "SC-nnn is Shortcut's display form")
	_, err := Open(":memory:", "sc", "Ada")
	assert.Error(t, err)
}

func TestCreateAndGet_defaultsAndKeyShape(t *testing.T) {
	c, now := newTestClient(t)
	ctx := context.Background()

	issue, err := c.CreateIssue(ctx, &tracker.Issue{Title: "  First  ", Description: "body", Labels: []string{"human/idea"}})
	require.NoError(t, err)
	assert.Equal(t, "LOC-1", issue.Key)
	assert.Equal(t, "LOC", issue.Project)
	assert.Equal(t, "First", issue.Title)
	assert.Equal(t, "Task", issue.Type)
	assert.Equal(t, "Backlog", issue.Status)
	assert.Equal(t, tracker.CategoryUnstarted, issue.StatusType)
	assert.Equal(t, "Ada", issue.Reporter)
	assert.Equal(t, []string{"human/idea"}, issue.Labels)
	assert.True(t, issue.IsIdea())
	assert.Equal(t, *now, issue.UpdatedAt)

	got, err := c.GetIssue(ctx, "loc-1")
	require.NoError(t, err, "keys match in any letter case")
	assert.Equal(t, issue.Key, got.Key)

	second := create(t, c, "Second")
	assert.Equal(t, "LOC-2", second.Key)
}

func TestCreate_refusesEmptyTitleAndForeignProject(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	_, err := c.CreateIssue(ctx, &tracker.Issue{Title: "   "})
	assert.Error(t, err)
	_, err = c.CreateIssue(ctx, nil)
	assert.Error(t, err)
	_, err = c.CreateIssue(ctx, &tracker.Issue{Title: "x", Project: "OTHER"})
	assert.Error(t, err, "a local tracker is one project; another name is a misroute")
	issue, err := c.CreateIssue(ctx, &tracker.Issue{Title: "x", Project: "loc"})
	require.NoError(t, err)
	assert.Equal(t, "LOC", issue.Project)
}

func TestCreate_bugTypeAndParent(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	parent := create(t, c, "Epic")
	bug, err := c.CreateIssue(ctx, &tracker.Issue{Title: "Crash", Type: "Bug", ParentKey: parent.Key, Status: "in progress"})
	require.NoError(t, err)
	assert.True(t, bug.IsBug())
	assert.Equal(t, parent.Key, bug.ParentKey)
	assert.Equal(t, "In Progress", bug.Status, "a known status is accepted in any case and stored canonically")

	_, err = c.CreateIssue(ctx, &tracker.Issue{Title: "orphan", ParentKey: "LOC-99"})
	assert.Error(t, err)
}

func TestGet_unknownAndForeignKeys(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	_, err := c.GetIssue(ctx, "LOC-7")
	assert.ErrorContains(t, err, "not found")
	for _, key := range []string{"SC-1", "octocat/repo#1", "LOC-0", "LOC-x", "LOC", ""} {
		_, err := c.GetIssue(ctx, key)
		assert.Error(t, err, key)
	}
}

func TestList_openOnlyByDefault_newestFirst_paged(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	a := create(t, c, "a")
	b := create(t, c, "b")
	d := create(t, c, "d")
	require.NoError(t, c.TransitionIssue(ctx, b.Key, "Done"))

	open, err := c.ListIssues(ctx, tracker.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{d.Key, a.Key}, keysOf(open))

	all, err := c.ListIssues(ctx, tracker.ListOptions{IncludeAll: true})
	require.NoError(t, err)
	assert.Equal(t, []string{d.Key, b.Key, a.Key}, keysOf(all))

	page, err := c.ListIssuesPage(ctx, tracker.ListOptions{IncludeAll: true, MaxResults: 2})
	require.NoError(t, err)
	assert.Len(t, page.Issues, 2)
	assert.True(t, page.Truncated)

	page, err = c.ListIssuesPage(ctx, tracker.ListOptions{IncludeAll: true, MaxResults: 3})
	require.NoError(t, err)
	assert.False(t, page.Truncated, "a fetch that ends exactly on the last row is complete")

	_, err = c.ListIssues(ctx, tracker.ListOptions{Project: "OTHER"})
	assert.Error(t, err)
	scoped, err := c.ListIssues(ctx, tracker.ListOptions{Project: "loc"})
	require.NoError(t, err)
	assert.Len(t, scoped, 2)
}

func TestList_updatedSince_movesOnCommentAndLink(t *testing.T) {
	c, now := newTestClient(t)
	ctx := context.Background()
	old := create(t, c, "old")
	cutoff := *now
	*now = now.Add(time.Minute)
	fresh := create(t, c, "fresh")

	since, err := c.ListIssues(ctx, tracker.ListOptions{UpdatedSince: cutoff})
	require.NoError(t, err)
	assert.Equal(t, []string{fresh.Key}, keysOf(since))

	*now = now.Add(time.Minute)
	_, err = c.AddComment(ctx, old.Key, "[human:plan]\nbody")
	require.NoError(t, err)
	since, err = c.ListIssues(ctx, tracker.ListOptions{UpdatedSince: cutoff})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{fresh.Key, old.Key}, keysOf(since), "a comment is a change the board polls for")
}

func TestComments_orderedAuthoredAndTimestamped(t *testing.T) {
	c, now := newTestClient(t)
	ctx := context.Background()
	issue := create(t, c, "x")
	first, err := c.AddComment(ctx, issue.Key, "one")
	require.NoError(t, err)
	*now = now.Add(time.Second)
	second, err := c.AddComment(ctx, issue.Key, "two")
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, "Ada", first.Author)

	comments, err := c.ListComments(ctx, issue.Key)
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Equal(t, "one", comments[0].Body)
	assert.Equal(t, "two", comments[1].Body)
	assert.True(t, comments[1].Created.After(comments[0].Created))

	_, err = c.AddComment(ctx, "LOC-9", "nope")
	assert.Error(t, err)
	_, err = c.ListComments(ctx, "LOC-9")
	assert.Error(t, err)
}

func TestLinks_relatedIsSymmetric_blocksIsDirected(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	a := create(t, c, "a")
	b := create(t, c, "b")
	d := create(t, c, "d")

	require.NoError(t, c.LinkIssues(ctx, b.Key, a.Key, tracker.LinkRelated))
	require.NoError(t, c.LinkIssues(ctx, a.Key, b.Key, tracker.LinkRelated), "linking the pair again stores nothing new")
	require.NoError(t, c.LinkIssues(ctx, a.Key, d.Key, tracker.LinkBlocks))

	got, err := c.GetIssue(ctx, a.Key)
	require.NoError(t, err)
	assert.ElementsMatch(t, []tracker.IssueLink{
		{Key: b.Key, Kind: tracker.LinkRelated},
		{Key: d.Key, Kind: tracker.LinkBlocks},
	}, got.Links)

	got, err = c.GetIssue(ctx, d.Key)
	require.NoError(t, err)
	assert.Equal(t, []tracker.IssueLink{{Key: a.Key, Kind: tracker.LinkBlocks, Inbound: true}}, got.Links)
	assert.Equal(t, []string{a.Key}, got.BlockedBy())

	listed, err := c.ListIssues(ctx, tracker.ListOptions{})
	require.NoError(t, err)
	for _, issue := range listed {
		if issue.Key == b.Key {
			assert.Equal(t, []tracker.IssueLink{{Key: a.Key, Kind: tracker.LinkRelated}}, issue.Links, "listings carry links too")
		}
	}

	require.NoError(t, c.UnlinkIssues(ctx, d.Key, a.Key), "unlink names the pair, not the direction")
	got, err = c.GetIssue(ctx, d.Key)
	require.NoError(t, err)
	assert.Empty(t, got.Links)

	assert.Error(t, c.LinkIssues(ctx, a.Key, a.Key, tracker.LinkRelated))
	assert.Error(t, c.LinkIssues(ctx, a.Key, b.Key, tracker.LinkKind("duplicates")))
	assert.Error(t, c.LinkIssues(ctx, a.Key, "LOC-42", tracker.LinkRelated))
}

func TestTransition_fixedWorkflow(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	issue := create(t, c, "x")

	statuses, err := c.ListStatuses(ctx, issue.Key)
	require.NoError(t, err)
	assert.Equal(t, []string{"Backlog", "In Progress", "Done", "Closed"}, namesOf(statuses))
	assert.Equal(t, tracker.CategoryClosed, statuses[3].Category)
	_, err = c.ListStatuses(ctx, "SC-1")
	assert.Error(t, err)

	require.NoError(t, c.TransitionIssue(ctx, issue.Key, "done"))
	got, err := c.GetIssue(ctx, issue.Key)
	require.NoError(t, err)
	assert.Equal(t, "Done", got.Status)
	assert.Equal(t, tracker.CategoryDone, got.StatusType)

	assert.Error(t, c.TransitionIssue(ctx, issue.Key, "Shipped"), "a status outside the fixed workflow is refused")
}

func TestAssignAndCurrentUser(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	issue := create(t, c, "x")
	me, err := c.GetCurrentUser(ctx)
	require.NoError(t, err)
	name, err := c.CurrentUserName(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Ada", me)
	assert.Equal(t, me, name)

	require.NoError(t, c.AssignIssue(ctx, issue.Key, me))
	got, err := c.GetIssue(ctx, issue.Key)
	require.NoError(t, err)
	assert.Equal(t, "Ada", got.Assignee)
	require.NoError(t, c.AssignIssue(ctx, issue.Key, ""))
	got, err = c.GetIssue(ctx, issue.Key)
	require.NoError(t, err)
	assert.Empty(t, got.Assignee)
}

func TestEdit_fieldsAndLabels(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	issue, err := c.CreateIssue(ctx, &tracker.Issue{Title: "x", Labels: []string{"human/idea", "ux"}})
	require.NoError(t, err)

	title, desc, typ := "New title", "new body", "Bug"
	got, err := c.EditIssue(ctx, issue.Key, tracker.EditOptions{
		Title: &title, Description: &desc, Type: &typ,
		AddLabels: []string{"security", "UX"}, RemoveLabels: []string{"Human/Idea"},
	})
	require.NoError(t, err)
	assert.Equal(t, "New title", got.Title)
	assert.Equal(t, "new body", got.Description)
	assert.Equal(t, "Bug", got.Type)
	assert.Equal(t, []string{"ux", "security"}, got.Labels, "removal is case-insensitive and a label is never stored twice")
	assert.False(t, got.IsIdea())
	assert.True(t, got.IsSecurity())

	empty := "  "
	_, err = c.EditIssue(ctx, issue.Key, tracker.EditOptions{Title: &empty})
	assert.Error(t, err)
	_, err = c.EditIssue(ctx, "LOC-8", tracker.EditOptions{})
	assert.Error(t, err)
}

func TestDelete_removesCommentsAndLinks(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	a := create(t, c, "a")
	b := create(t, c, "b")
	_, err := c.AddComment(ctx, a.Key, "note")
	require.NoError(t, err)
	require.NoError(t, c.LinkIssues(ctx, a.Key, b.Key, tracker.LinkBlocks))

	require.NoError(t, c.DeleteIssue(ctx, a.Key))
	_, err = c.GetIssue(ctx, a.Key)
	assert.Error(t, err)
	got, err := c.GetIssue(ctx, b.Key)
	require.NoError(t, err)
	assert.Empty(t, got.Links, "a link to a deleted issue does not survive it")
	assert.Error(t, c.DeleteIssue(ctx, a.Key))

	next := create(t, c, "c")
	assert.Equal(t, "LOC-3", next.Key, "numbers are never reused after a delete")
}

func TestOpen_persistsOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "tickets.db")
	c, err := Open(path, "LOC", "Ada")
	require.NoError(t, err)
	created := create(t, c, "kept")
	require.NoError(t, c.Close())

	again, err := Open(path, "LOC", "Ada")
	require.NoError(t, err)
	defer func() { _ = again.Close() }()
	got, err := again.GetIssue(context.Background(), created.Key)
	require.NoError(t, err)
	assert.Equal(t, "kept", got.Title)
	assert.Equal(t, path, again.Path())
	assert.Equal(t, "LOC", again.Prefix())
}

func keysOf(issues []tracker.Issue) []string {
	out := make([]string, len(issues))
	for i, issue := range issues {
		out[i] = issue.Key
	}
	return out
}

func namesOf(statuses []tracker.Status) []string {
	out := make([]string, len(statuses))
	for i, st := range statuses {
		out[i] = st.Name
	}
	return out
}
