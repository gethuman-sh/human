package daemon

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerDeps wires a transition engine whose only interesting collaborators are
// the launcher and the ownership hook.
func ownerDeps(l *fakeLauncher, owned *[]string, ownErr error) BoardTransitionDeps {
	return BoardTransitionDeps{
		Commenter: &fakeCommenter{},
		Launcher:  l,
		SetTicketOwner: func(pmKey string) error {
			*owned = append(*owned, pmKey)
			return ownErr
		},
		WorkspaceDir: "/ws",
		ConfigDir:    "/ws",
	}
}

func TestLaunchAgentTakesOwnershipForThisMachine(t *testing.T) {
	var owned []string
	deps := ownerDeps(&fakeLauncher{}, &owned, nil)

	launched, err := deps.launchAgent(context.Background(), "SC-7", "board-SC-7-planning", "/human-plan SC-7")

	require.NoError(t, err)
	assert.True(t, launched, "a launch that started an agent says so")
	assert.Equal(t, []string{"SC-7"}, owned, "starting work on a ticket records who is working it")
}

// A single-flight refusal means an agent is already on it: that run's ownership
// is the accurate one, and this call started nothing to own.
func TestLaunchAgentSkipsOwnershipWhenAnAgentIsAlreadyRunning(t *testing.T) {
	var owned []string
	deps := ownerDeps(&fakeLauncher{err: ErrAgentAlreadyRunning}, &owned, nil)

	launched, err := deps.launchAgent(context.Background(), "SC-7", "board-SC-7-planning", "prompt")

	require.NoError(t, err, "a refusal is not a failure")
	assert.False(t, launched, "nothing started here, and every caller keys its marker off that")
	assert.Empty(t, owned)
}

func TestLaunchAgentSkipsOwnershipWhenTheLaunchFailed(t *testing.T) {
	var owned []string
	boom := stderrors.New("no container")
	deps := ownerDeps(&fakeLauncher{err: boom}, &owned, nil)

	launched, err := deps.launchAgent(context.Background(), "SC-7", "board-SC-7-planning", "prompt")

	require.ErrorIs(t, err, boom)
	assert.False(t, launched)
	assert.Empty(t, owned, "nothing is working the ticket, so nothing owns it")
}

// Ownership records work, it never gates it: a tracker that refuses must not
// fail a launch that already succeeded.
func TestLaunchAgentSucceedsWhenTakingOwnershipFails(t *testing.T) {
	var owned []string
	deps := ownerDeps(&fakeLauncher{}, &owned, stderrors.New("tracker down"))

	launched, err := deps.launchAgent(context.Background(), "SC-7", "board-SC-7-planning", "prompt")

	require.NoError(t, err)
	assert.True(t, launched)
	assert.Equal(t, []string{"SC-7"}, owned, "it was attempted")
}

// An un-wired daemon still runs every stage; it simply records no ownership.
func TestLaunchAgentWithoutAnOwnerHookStillLaunches(t *testing.T) {
	l := &fakeLauncher{}
	deps := BoardTransitionDeps{Commenter: &fakeCommenter{}, Launcher: l, WorkspaceDir: "/ws", ConfigDir: "/ws"}

	launched, err := deps.launchAgent(context.Background(), "SC-7", "board-SC-7-planning", "prompt")

	require.NoError(t, err)
	assert.True(t, launched)
	assert.Equal(t, 1, l.calls)
}

func TestSetTicketOwnerIgnoresAnEmptyKey(t *testing.T) {
	var owned []string
	deps := ownerDeps(&fakeLauncher{}, &owned, nil)

	deps.setTicketOwner("")

	assert.Empty(t, owned)
}

// OwnerOf is the one definition of "whose ticket is this?" — the daemon's work
// gate and the board's dimming overlay both read it, so the cases are pinned
// here rather than in either caller.
func TestOwnerOf(t *testing.T) {
	t.Run("assignee wins", func(t *testing.T) {
		assert.Equal(t, "Alice", OwnerOf("Alice", "Bob"))
	})
	t.Run("reporter when unassigned", func(t *testing.T) {
		assert.Equal(t, "Bob", OwnerOf("", "Bob"))
	})
	t.Run("whitespace is not an assignee", func(t *testing.T) {
		assert.Equal(t, "Bob", OwnerOf("   ", "Bob"))
	})
	// Neither field recorded is "owner unknown", not "owned by nobody": the empty
	// string is what every caller keys its unknown-owner branch off.
	t.Run("neither recorded is empty", func(t *testing.T) {
		assert.Empty(t, OwnerOf("", ""))
	})
}
