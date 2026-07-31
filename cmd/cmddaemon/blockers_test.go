package cmddaemon

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// blockerGetter answers with a canned status per key; a key with no entry
// fails, standing in for a blocker the tracker will not return.
type blockerGetter struct {
	status map[string]tracker.Category
}

func (g blockerGetter) GetIssue(_ context.Context, key string) (*tracker.Issue, error) {
	cat, ok := g.status[key]
	if !ok {
		return nil, errors.WithDetails("no such issue", "key", key)
	}
	return &tracker.Issue{Key: key, StatusType: cat}, nil
}

func blocked(keys ...string) *tracker.Issue {
	issue := &tracker.Issue{Key: "SC-1"}
	for _, k := range keys {
		issue.Links = append(issue.Links, tracker.IssueLink{Key: k, Kind: tracker.LinkBlocks, Inbound: true})
	}
	return issue
}

// Only work that is still open holds anything back — a blocker that shipped, or
// one that was cancelled, is finished either way and must not keep a card
// waiting forever.
func TestOpenBlockers_finishedBlockersAreDropped(t *testing.T) {
	getter := blockerGetter{status: map[string]tracker.Category{
		"SC-2": tracker.CategoryDone,
		"SC-3": tracker.CategoryClosed,
		"SC-4": tracker.CategoryStarted,
	}}

	open := openBlockers(context.Background(), getter, blocked("SC-2", "SC-3", "SC-4"), zerolog.Nop())

	assert.Equal(t, []string{"SC-4"}, open)
}

// Not being able to read a blocker says nothing about whether it finished, so
// the work stays held. The alternative — starting on the assumption it is done —
// is exactly the collision the dependency was written to prevent.
func TestOpenBlockers_unreadableBlockerStillHolds(t *testing.T) {
	getter := blockerGetter{status: map[string]tracker.Category{}}

	open := openBlockers(context.Background(), getter, blocked("SC-2"), zerolog.Nop())

	assert.Equal(t, []string{"SC-2"}, open)
}

// A ticket that merely relates to another, or that blocks one, is not waiting
// for anything.
func TestOpenBlockers_onlyInboundBlocksCount(t *testing.T) {
	getter := blockerGetter{status: map[string]tracker.Category{"SC-2": tracker.CategoryStarted}}
	issue := &tracker.Issue{Key: "SC-1", Links: []tracker.IssueLink{
		{Key: "SC-2", Kind: tracker.LinkBlocks, Inbound: false},
		{Key: "SC-3", Kind: tracker.LinkRelated, Inbound: true},
	}}

	assert.Empty(t, openBlockers(context.Background(), getter, issue, zerolog.Nop()))
}
