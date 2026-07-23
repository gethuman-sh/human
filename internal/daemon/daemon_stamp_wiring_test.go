package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// A daemon-posted started marker carries the daemon's id so a teammate can tell
// which machine's bot launched the stage (SC-660 rule 1).
func TestStartAgentStage_stampsStartedMarker(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.DaemonID = "d1"

	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardBacklog, To: BoardPlanning})
	require.NoError(t, err)

	// A provisioned daemon claims the stage before starting it (SC-660 rule 2),
	// so the thread carries the stamped claim followed by the stamped started
	// marker.
	require.Len(t, c.added, 2)
	assert.Contains(t, c.added[0], ClaimHeader)
	assert.Equal(t, "d1", ParseDaemonID(c.added[0]))
	assert.Contains(t, c.added[1], PlanningStartedHeader)
	assert.Equal(t, "d1", ParseDaemonID(c.added[1]))
}

// The failure watcher's *-failed marker is stamped too, so a crash is attributed
// to the machine whose agent died.
func TestHandleBoardAgentExit_stampsFailedMarker(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:implementation-started]", time.Unix(1, 0))}}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }

	handleBoardAgentExit(context.Background(), "board-SC-1-implementation", "",
		commenterFor, nil, alwaysReachable, nil, nil, nil, StageRetry{}, "d1", zerolog.Nop())

	require.Len(t, c.added, 1)
	assert.Contains(t, c.added[0], ImplementationFailedHeader)
	assert.Equal(t, "d1", ParseDaemonID(c.added[0]))
}
