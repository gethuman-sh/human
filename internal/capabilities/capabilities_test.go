package capabilities

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/env"
)

func withAgent(name string) context.Context {
	return env.WithEnv(context.Background(), map[string]string{"HUMAN_AGENT_NAME": name})
}

func hasRemote(context.Context) bool { return true }
func noRemote(context.Context) bool  { return false }

// A board stage agent holds no push credentials and the board's Deploy stage
// owns shipping, so every shipping capability is withheld — and the set says
// why, so a stage that stops can quote the reason.
func TestDetect_BoardAgentMayNotShip(t *testing.T) {
	set := Detect(withAgent("board-SC-9-implementation"), hasRemote)

	require.True(t, set.BoardContext)
	require.False(t, set.CanPush)
	require.False(t, set.CanOpenPR)
	require.False(t, set.OwnsDeploy)
	require.Equal(t, WorkspaceBindMounted, set.Workspace)
	require.Contains(t, set.Reason, "no push credentials")
}

// The board signal is the daemon's agent-name prefix; an agent named anything
// else is a standalone run.
func TestDetect_StandaloneRunMayShip(t *testing.T) {
	set := Detect(withAgent("autofix-local"), hasRemote)

	require.False(t, set.BoardContext)
	require.True(t, set.CanPush)
	require.True(t, set.CanOpenPR)
	require.True(t, set.OwnsDeploy)
	require.Equal(t, WorkspaceLocal, set.Workspace)
	require.Empty(t, set.Reason)
}

func TestDetect_NoAgentNameIsStandalone(t *testing.T) {
	set := Detect(context.Background(), hasRemote)

	require.False(t, set.BoardContext)
	require.True(t, set.CanPush)
}

func TestDetect_WithoutARemoteNothingCanShip(t *testing.T) {
	set := Detect(withAgent("autofix-local"), noRemote)

	require.False(t, set.CanPush)
	require.False(t, set.CanOpenPR)
	require.False(t, set.OwnsDeploy)
	require.Contains(t, set.Reason, "no push remote")
}

// A missing probe must withhold the capability rather than assume it: guessing
// wrong in that direction ends with an agent trying to push from a container
// that cannot.
func TestDetect_NilProbeWithholdsPush(t *testing.T) {
	set := Detect(context.Background(), nil)

	require.False(t, set.CanPush)
	require.NotEmpty(t, set.Reason)
}

// "board-" is a prefix match on the daemon's naming scheme; a name that merely
// contains it elsewhere is not a board agent.
func TestDetect_BoardPrefixIsNotASubstringMatch(t *testing.T) {
	set := Detect(withAgent("my-board-helper"), hasRemote)

	require.False(t, set.BoardContext)
	require.True(t, set.CanPush)
}

func TestGitRemoteProbe_RunsWithoutPanicking(t *testing.T) {
	// The repo under test has a remote; in a checkout without one the probe
	// must still answer rather than fail.
	require.NotPanics(t, func() { _ = GitRemoteProbe(context.Background()) })
}
