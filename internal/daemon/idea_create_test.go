package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestIdeaCreator_CreateIdea(t *testing.T) {
	creator := newFakeCreator()
	notified := 0
	c := newTestIdeaCreator(creator, "proj", func() { notified++ })

	key, url, err := c.CreateIdea("Weekly digest email")
	require.NoError(t, err)
	assert.Equal(t, "SC-999", key)
	assert.NotEmpty(t, url)
	assert.Equal(t, 1, notified, "capture must poke the subscribe loop so the card appears")

	captured := creator.capturedIssue()
	require.NotNil(t, captured)
	assert.Equal(t, "proj", captured.Project)
	assert.Equal(t, "Weekly digest email", captured.Title)
	assert.Equal(t, []string{tracker.IdeaLabel}, captured.Labels)
}

func TestIdeaCreator_CreateIdeaEmptyTitle(t *testing.T) {
	c := newTestIdeaCreator(newFakeCreator(), "proj", nil)
	_, _, err := c.CreateIdea("   ")
	require.Error(t, err)
}

func TestIdeaCreator_CreateIdeaNilResolver(t *testing.T) {
	c := &IdeaCreator{}
	_, _, err := c.CreateIdea("an idea")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no PM ticket creator configured")
}

func TestIdeaCreator_CreateIdeaResolverError(t *testing.T) {
	c := &IdeaCreator{ResolveCreator: func() (tracker.Creator, string, error) {
		return nil, "", assert.AnError
	}}
	_, _, err := c.CreateIdea("an idea")
	require.Error(t, err)
}

func TestIdeaCreator_CreateIdeaTrackerError(t *testing.T) {
	creator := &fakeCreator{err: assert.AnError}
	c := newTestIdeaCreator(creator, "proj", func() { t.Fatal("a failed create must not poke the board") })
	_, _, err := c.CreateIdea("an idea")
	require.Error(t, err)
}

// A provider that returns (nil, nil) breaks its contract; that must surface as
// an error rather than a nil dereference.
func TestIdeaCreator_CreateIdeaNilIssue(t *testing.T) {
	c := newTestIdeaCreator(&fakeCreator{}, "proj", nil)
	_, _, err := c.CreateIdea("an idea")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker returned no issue")
}
