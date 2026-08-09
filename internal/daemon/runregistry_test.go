package daemon

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SC-4082. The daemon must act on runs it started, not on whatever an event
// names. These pin the registry; claimExit below pins the decision that uses it.
func TestRunRegistry(t *testing.T) {
	t.Run("a registered run is claimable once", func(t *testing.T) {
		r := NewRunRegistry()
		id := r.Register("board-SC-1-implementation", "SC-1", BoardImplementation)
		require.NotEmpty(t, id)

		rec, ok := r.Claim(id)
		require.True(t, ok)
		assert.Equal(t, "SC-1", rec.PMKey)
		assert.Equal(t, BoardImplementation, rec.Stage)

		// The exactly-once gate: one run can raise several events that all look
		// like its exit, and only the first may move the ticket.
		_, ok = r.Claim(id)
		assert.False(t, ok, "a second event for the same run claims nothing")
	})

	t.Run("an unminted id is never claimable", func(t *testing.T) {
		r := NewRunRegistry()
		r.Register("board-SC-1-implementation", "SC-1", BoardImplementation)

		_, ok := r.Claim("0000000000000000000000000000000000")
		assert.False(t, ok, "a run this daemon did not start is not its to act on")
	})

	t.Run("ids are unguessable and distinct", func(t *testing.T) {
		r := NewRunRegistry()
		seen := map[string]bool{}
		for range 100 {
			id := r.Register("board-SC-1-implementation", "SC-1", BoardImplementation)
			require.Len(t, id, 32, "128 bits, hex")
			assert.False(t, seen[id], "ids must not repeat")
			seen[id] = true
		}
	})

	t.Run("a failed launch forgets its id", func(t *testing.T) {
		r := NewRunRegistry()
		id := r.Register("board-SC-1-implementation", "SC-1", BoardImplementation)
		r.Forget(id)

		_, ok := r.Claim(id)
		assert.False(t, ok)
		assert.Zero(t, r.Len(), "a run that never started leaves no record behind")
	})

	// nil disables, the package convention: a partially wired daemon still runs.
	t.Run("a nil registry is inert", func(t *testing.T) {
		var r *RunRegistry
		assert.Empty(t, r.Register("a", "SC-1", BoardImplementation))
		_, ok := r.Claim("anything")
		assert.False(t, ok)
		assert.Zero(t, r.Len())
	})
}

// claimExit is where the event stops being trusted. The ticket comes from the
// daemon's own launch record; an event for a run it never started moves nothing.
func TestClaimExit(t *testing.T) {
	t.Run("the ticket comes from the launch record, not the name", func(t *testing.T) {
		r := NewRunRegistry()
		id := r.Register("board-SC-1-implementation", "SC-1", BoardImplementation)

		// The event names a DIFFERENT ticket than the one actually launched — the
		// forged-name case. The record wins.
		pmKey, stage, ok := claimExit(r, id, "board-SC-999-planning", zerolog.Nop())

		require.True(t, ok)
		assert.Equal(t, "SC-1", pmKey, "the daemon acts on what it launched")
		assert.Equal(t, BoardImplementation, stage)
	})

	t.Run("an event for an unlaunched run is refused", func(t *testing.T) {
		r := NewRunRegistry()
		_, _, ok := claimExit(r, "deadbeef", "board-SC-1-implementation", zerolog.Nop())
		assert.False(t, ok)
	})

	t.Run("a second event for the same run is refused", func(t *testing.T) {
		r := NewRunRegistry()
		id := r.Register("board-SC-1-implementation", "SC-1", BoardImplementation)

		_, _, first := claimExit(r, id, "board-SC-1-implementation", zerolog.Nop())
		_, _, second := claimExit(r, id, "board-SC-1-implementation", zerolog.Nop())

		assert.True(t, first)
		assert.False(t, second, "the escalation is posted once, not once per event")
	})

	// A run launched before this daemon started carries no id. Dropping those
	// exits would strand every in-flight card across an upgrade, so they fall back
	// to the name — the window is one restart wide and closes itself.
	t.Run("a run with no id falls back to the name", func(t *testing.T) {
		pmKey, stage, ok := claimExit(NewRunRegistry(), "", "board-SC-1-implementation", zerolog.Nop())

		require.True(t, ok)
		assert.Equal(t, "SC-1", pmKey)
		assert.Equal(t, BoardImplementation, stage)
	})

	t.Run("an unparseable name with no id is still refused", func(t *testing.T) {
		_, _, ok := claimExit(NewRunRegistry(), "", "not-a-board-agent", zerolog.Nop())
		assert.False(t, ok)
	})
}

// The launch side: every board launch registers, and the container is told which
// run it is so its hooks can carry the id back.
func TestLaunchAgent_RegistersTheRun(t *testing.T) {
	l := &fakeLauncher{}
	deps := BoardTransitionDeps{
		Commenter: &fakeCommenter{},
		Launcher:  l,
		Runs:      NewRunRegistry(),
	}

	require.NoError(t, deps.launchAgent(context.Background(), "SC-1", "board-SC-1-implementation", "/do-it"))

	require.NotEmpty(t, l.runID, "the container must be told which run it is")
	rec, ok := deps.Runs.Claim(l.runID)
	require.True(t, ok, "the launch must be registered before the run can fire an event")
	assert.Equal(t, "SC-1", rec.PMKey)
	assert.Equal(t, BoardImplementation, rec.Stage)
}

// A launch that did not happen must leave no record: nothing will ever arrive
// for it, and the entry would pin memory and a claimable id forever.
func TestLaunchAgent_FailedLaunchForgetsTheRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"hard failure", assert.AnError},
		{"single-flight refusal", ErrAgentAlreadyRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := BoardTransitionDeps{
				Commenter: &fakeCommenter{},
				Launcher:  &fakeLauncher{err: tc.err},
				Runs:      NewRunRegistry(),
			}

			_ = deps.launchAgent(context.Background(), "SC-1", "board-SC-1-implementation", "/do-it")

			assert.Zero(t, deps.Runs.Len(), "no record survives a launch that did not start")
		})
	}
}
