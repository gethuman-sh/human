package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// A daemon-posted started marker carries the daemon's id so a teammate can tell
// which machine's bot launched the stage (SC-660 rule 1). Provenance is no
// longer hand-stamped in the transition logic; it is signed at the commenter
// choke point (the daemon wires marker.NewSigningCommenter), so the test drives
// the transition through a signing commenter and reads the id back off the
// machine: field.
func TestStartAgentStage_stampsStartedMarker(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.Commenter = marker.NewSigningCommenter(c, "d1", "rev1")
	deps.DaemonID = "d1"

	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardBacklog, To: BoardPlanning})
	require.NoError(t, err)

	// A provisioned daemon claims the stage before starting it (SC-660 rule 2),
	// so the thread carries the signed claim followed by the signed started
	// marker.
	require.Len(t, c.added, 2)
	assert.Contains(t, c.added[0], ClaimHeader)
	assert.Equal(t, "d1", ParseDaemonID(c.added[0]))
	assert.Equal(t, "rev1", ParseBuild(c.added[0]))
	assert.Contains(t, c.added[1], PlanningStartedHeader)
	assert.Equal(t, "d1", ParseDaemonID(c.added[1]))
	assert.Equal(t, "rev1", ParseBuild(c.added[1]))
}

// The failure watcher's *-failed marker is signed too, so a crash is attributed
// to the machine whose agent died — again via the signing commenter the daemon
// resolves for the watcher.
func TestHandleBoardAgentExit_stampsFailedMarker(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:implementation-started]", time.Unix(1, 0))}}
	signing := marker.NewSigningCommenter(c, "d1", "rev1")
	commenterFor := func() (tracker.Commenter, error) { return signing, nil }

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "", "",
		commenterFor, nil, nil, nil, alwaysReachable, nil, nil, nil, StageRetry{}, nil, "d1", zerolog.Nop())

	require.Len(t, c.added, 1)
	assert.Contains(t, c.added[0], ImplementationFailedHeader)
	assert.Equal(t, "d1", ParseDaemonID(c.added[0]))
	assert.Equal(t, "rev1", ParseBuild(c.added[0]))
}
