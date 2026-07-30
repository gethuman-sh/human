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

// The PR-review→fix loop's fixer is the one board stage that must push: it
// commits the reviewer's change to its own PR branch and the reviewer re-reads
// the pushed head, so a fixer that cannot push deadlocks the loop (SC-1760). It
// may push (with a reachable remote) but still may neither open a PR nor deploy.
func TestDetect_BoardPRFixerMayPushItsOwnBranch(t *testing.T) {
	set := Detect(withAgent("board-SC-1760-prfix"), hasRemote)

	require.True(t, set.BoardContext)
	require.True(t, set.CanPush, "the fixer must push its own PR branch so the loop converges")
	require.False(t, set.CanOpenPR, "opening PRs stays the Deploy stage's")
	require.False(t, set.OwnsDeploy, "deploying stays the Deploy stage's")
	require.Equal(t, WorkspaceBindMounted, set.Workspace)
	require.Contains(t, set.Reason, "PR-fixer")
}

// The fixer's push is gated on the same reachable-remote probe a standalone run
// passes: a container with no usable remote still withholds push, since a
// withheld capability is a boundary while a wrongly granted one fails at push.
func TestDetect_BoardPRFixerWithoutARemoteCannotPush(t *testing.T) {
	set := Detect(withAgent("board-SC-1760-prfix"), noRemote)

	require.True(t, set.BoardContext)
	require.False(t, set.CanPush)
	require.Contains(t, set.Reason, "no reachable remote")
}

// A nil probe withholds the fixer's push exactly as it does a standalone run's:
// guessing "yes" ends with a push from a container that cannot reach its remote.
func TestDetect_BoardPRFixerNilProbeWithholdsPush(t *testing.T) {
	set := Detect(withAgent("board-SC-1760-prfix"), nil)

	require.False(t, set.CanPush)
	require.NotEmpty(t, set.Reason)
}

// The carve-out is the fixer alone: every other board stage stays
// credential-restricted even with a reachable remote.
func TestDetect_OtherBoardStagesStillMayNotPush(t *testing.T) {
	for _, stage := range []string{"implementation", "prreview", "deploy", "deployfix"} {
		set := Detect(withAgent("board-SC-1760-"+stage), hasRemote)
		require.Falsef(t, set.CanPush, "stage %q must not push from a board container", stage)
		require.Contains(t, set.Reason, "no push credentials")
	}
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
	require.Contains(t, set.Reason, "no reachable remote")
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

// A directory that is not a git repository has no reachable remote, so the
// probe must answer false rather than error out or hang.
func TestGitRemoteProbe_NonRepoIsNotReachable(t *testing.T) {
	t.Chdir(t.TempDir())
	require.False(t, GitRemoteProbe(context.Background()))
}
