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

	require.NoError(t, deps.launchAgent(context.Background(), "SC-7", "board-SC-7-planning", "/human-plan SC-7"))

	assert.Equal(t, []string{"SC-7"}, owned, "starting work on a ticket records who is working it")
}

// A single-flight refusal means an agent is already on it: that run's ownership
// is the accurate one, and this call started nothing to own.
func TestLaunchAgentSkipsOwnershipWhenAnAgentIsAlreadyRunning(t *testing.T) {
	var owned []string
	deps := ownerDeps(&fakeLauncher{err: ErrAgentAlreadyRunning}, &owned, nil)

	require.NoError(t, deps.launchAgent(context.Background(), "SC-7", "board-SC-7-planning", "prompt"))

	assert.Empty(t, owned)
}

func TestLaunchAgentSkipsOwnershipWhenTheLaunchFailed(t *testing.T) {
	var owned []string
	boom := stderrors.New("no container")
	deps := ownerDeps(&fakeLauncher{err: boom}, &owned, nil)

	require.ErrorIs(t, deps.launchAgent(context.Background(), "SC-7", "board-SC-7-planning", "prompt"), boom)

	assert.Empty(t, owned, "nothing is working the ticket, so nothing owns it")
}

// Ownership records work, it never gates it: a tracker that refuses must not
// fail a launch that already succeeded.
func TestLaunchAgentSucceedsWhenTakingOwnershipFails(t *testing.T) {
	var owned []string
	deps := ownerDeps(&fakeLauncher{}, &owned, stderrors.New("tracker down"))

	require.NoError(t, deps.launchAgent(context.Background(), "SC-7", "board-SC-7-planning", "prompt"))

	assert.Equal(t, []string{"SC-7"}, owned, "it was attempted")
}

// An un-wired daemon still runs every stage; it simply records no ownership.
func TestLaunchAgentWithoutAnOwnerHookStillLaunches(t *testing.T) {
	l := &fakeLauncher{}
	deps := BoardTransitionDeps{Commenter: &fakeCommenter{}, Launcher: l, WorkspaceDir: "/ws", ConfigDir: "/ws"}

	require.NoError(t, deps.launchAgent(context.Background(), "SC-7", "board-SC-7-planning", "prompt"))

	assert.Equal(t, 1, l.calls)
}

func TestSetTicketOwnerIgnoresAnEmptyKey(t *testing.T) {
	var owned []string
	deps := ownerDeps(&fakeLauncher{}, &owned, nil)

	deps.setTicketOwner("")

	assert.Empty(t, owned)
}
