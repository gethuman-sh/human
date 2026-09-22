package daemon

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/pipelinefsm"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/tracker/local"
)

// A real board transition against a real tracker: the local store on
// ":memory:", in strict mode, so every marker the daemon posts has to be one
// the pipeline state machine allows from where the ticket is. This is the
// shape that replaces a stubbed commenter — the comments the pass reads are
// the ones the code under test wrote, through the path a live ticket takes.
func newLocalDeps(t *testing.T) (*local.Client, *fakeLauncher, BoardTransitionDeps) {
	t.Helper()
	c, err := local.OpenMemory("LOC", "Ada", local.Strict())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	l := &fakeLauncher{}
	deps := BoardTransitionDeps{Commenter: c, Getter: c, Launcher: l, Deployer: &fakeDeployer{}, WorkspaceDir: "/ws", ConfigDir: "/ws"}
	return c, l, deps
}

// The queued-card recovery that SC-4397 fixed, driven end to end: a planner
// asked, the decision was answered, the stage never restarted, ApplyTransition
// on the same column launches it — and the [human:planning-started] it posts lands in the
// store's index, admitted by the strict machine from `queued`.
func TestApplyTransition_QueuedCard_OnLocalTracker(t *testing.T) {
	c, l, deps := newLocalDeps(t)
	ctx := context.Background()
	key, err := local.SeedMarkers(ctx, c, "queued planning", []marker.Marker{
		{Type: "planning-started"},
		{Type: "options", Fields: map[string]string{"stage": "planning"}, Body: "1: this way\n2: that way"},
		{Type: "option-chosen", Head: "1: this way", Fields: map[string]string{"stage": "planning"}},
	})
	require.NoError(t, err)

	require.NoError(t, deps.ApplyTransition(ctx, BoardTransitionRequest{PMKey: key, From: BoardPlanning, To: BoardPlanning}))

	assert.Equal(t, 1, l.calls, "the queued stage starts")
	assert.Contains(t, l.prompt, "/human-ticket-review "+key)

	markers, err := c.ListMarkers(ctx, key)
	require.NoError(t, err)
	types := make([]string, len(markers))
	for i, m := range markers {
		types[i] = m.Type
	}
	assert.Equal(t, []string{"planning-started", "options", "option-chosen", "planning-started"}, types)

	// The strict store is the second reader of the document; the daemon's own
	// derivation agrees with it about where the ticket now is.
	comments, err := c.ListComments(ctx, key)
	require.NoError(t, err)
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardPlanning, card.Stage)
	assert.Equal(t, BoardRunning, card.State)
}

// A history from the replay corpus seeds a ticket at the point it got stuck;
// the daemon then acts on it as it would on the live one. SC-1402 ends in
// nothing-to-do, which is terminal: a forward move must be refused, and refused
// before any marker is posted, so the strict store sees no write at all.
func TestApplyTransition_CorpusTrace_OnLocalTracker(t *testing.T) {
	c, l, deps := newLocalDeps(t)
	ctx := context.Background()
	key, err := local.SeedTrace(ctx, c, pipelinefsm.Trace{Key: "SC-1402", Markers: []string{
		"planning-started", "claim", "ticket-review-started", "planning-failed",
		"planning-started", "claim", "ticket-review-started", "ticket-review", "nothing-to-do",
	}})
	require.NoError(t, err)
	before, err := c.Version(ctx)
	require.NoError(t, err)

	err = deps.ApplyTransition(ctx, BoardTransitionRequest{PMKey: key, From: BoardPlanning, To: BoardImplementation})
	assert.Error(t, err)
	assert.Equal(t, 0, l.calls)

	after, err := c.Version(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a refused move writes nothing")
}
