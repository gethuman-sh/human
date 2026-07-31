package shortcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// Shortcut states a relation as subject-verb-object and sends the same record
// to both stories, so which end this story sits on decides the direction. A
// caller that got this backwards would gate the wrong ticket — stalling work
// that was ready and releasing work that was not.
func TestStoryLinks_DirectionIsResolvedAgainstThisStory(t *testing.T) {
	link := scStoryLink{Verb: "blocks", SubjectID: 100, ObjectID: 200}

	blocker := storyLinks(scStory{ID: 100, StoryLinks: []scStoryLink{link}})
	require.Len(t, blocker, 1)
	assert.Equal(t, "SC-200", blocker[0].Key)
	assert.False(t, blocker[0].Inbound, "the subject blocks; it does not wait")

	blocked := storyLinks(scStory{ID: 200, StoryLinks: []scStoryLink{link}})
	require.Len(t, blocked, 1)
	assert.Equal(t, "SC-100", blocked[0].Key)
	assert.True(t, blocked[0].Inbound, "the object is the one that must wait")
}

// BlockedBy is what the gate reads, so it must return only the links that mean
// "this issue must wait" — not everything it is merely associated with.
func TestIssue_BlockedByReturnsOnlyInboundBlocks(t *testing.T) {
	issue := tracker.Issue{Links: []tracker.IssueLink{
		{Key: "SC-1", Kind: tracker.LinkBlocks, Inbound: true},  // waits for SC-1
		{Key: "SC-2", Kind: tracker.LinkBlocks, Inbound: false}, // blocks SC-2
		{Key: "SC-3", Kind: tracker.LinkRelated, Inbound: true}, // merely related
	}}

	assert.Equal(t, []string{"SC-1"}, issue.BlockedBy())
}

// A story both blocking and blocked resolves both directions independently.
func TestStoryLinks_BothDirectionsAtOnce(t *testing.T) {
	links := storyLinks(scStory{ID: 100, StoryLinks: []scStoryLink{
		{Verb: "blocks", SubjectID: 100, ObjectID: 200},
		{Verb: "blocks", SubjectID: 50, ObjectID: 100},
	}})

	require.Len(t, links, 2)
	assert.Equal(t, tracker.Issue{Links: links}.BlockedBy(), []string{"SC-50"},
		"only the relation where this story is the object makes it wait")
}

// A verb we do not model is dropped rather than guessed at. Reporting a
// relationship we cannot describe is worse than reporting none, because the
// gate would act on it — and "duplicates" in particular says two tickets are
// the same work, not that one waits for the other.
func TestStoryLinks_UnmodelledVerbsAreDropped(t *testing.T) {
	links := storyLinks(scStory{ID: 100, StoryLinks: []scStoryLink{
		{Verb: "duplicates", SubjectID: 100, ObjectID: 200},
		{Verb: "", SubjectID: 100, ObjectID: 300},
	}})

	assert.Empty(t, links)
}

func TestStoryLinks_RelatesToIsSymmetric(t *testing.T) {
	links := storyLinks(scStory{ID: 100, StoryLinks: []scStoryLink{
		{Verb: "relates to", SubjectID: 100, ObjectID: 200},
	}})

	require.Len(t, links, 1)
	assert.Equal(t, tracker.LinkRelated, links[0].Kind)
	assert.Empty(t, tracker.Issue{Links: links}.BlockedBy(), "an association orders nothing")
}

// A relation naming neither this story is not ours to report.
func TestStoryLinks_UnrelatedRecordIsIgnored(t *testing.T) {
	assert.Empty(t, storyLinks(scStory{ID: 100, StoryLinks: []scStoryLink{
		{Verb: "blocks", SubjectID: 1, ObjectID: 2},
	}}))
}

// A story with no relations, or a backend that omits the field entirely, yields
// no links — which is a correct answer, not a gap. This is also the shape a
// payload takes if Shortcut turns out not to send story_links at all.
func TestStoryLinks_AbsentFieldYieldsNothing(t *testing.T) {
	assert.Empty(t, storyLinks(scStory{ID: 100}))
}
