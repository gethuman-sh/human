package daemon

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDaemonForwardedStatesMatchFrontend is the daemon half of the two-way
// drift lock (SC-3024): the frontend's DAEMON_FORWARDED_STATES list
// (desktop/frontend/src/board-states.ts) must name exactly the daemon's
// forwardable BoardState constants — BoardIdle ("") excluded, the resting
// default every card starts from rather than a state the daemon ever forwards
// as news. Adding a state on either side without teaching the other fails
// this test or its frontend twin (board-queue.test.mjs's "badgeInfo covers
// every daemon-forwarded state").
func TestDaemonForwardedStatesMatchFrontend(t *testing.T) {
	data, err := os.ReadFile("../../desktop/frontend/src/board-states.ts")
	require.NoError(t, err, "board-states.ts must exist and be readable from internal/daemon")

	m := regexp.MustCompile(`DAEMON_FORWARDED_STATES\s*=\s*\[([^\]]*)\]`).FindSubmatch(data)
	require.NotNil(t, m, "board-states.ts must define DAEMON_FORWARDED_STATES = [...]")

	literals := regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1)
	got := make(map[string]bool, len(literals))
	for _, lit := range literals {
		got[string(lit[1])] = true
	}

	want := map[string]bool{
		string(BoardRunning):  true,
		string(BoardQueued):   true,
		string(BoardDone):     true,
		string(BoardFailed):   true,
		string(BoardResolved): true,
		string(BoardOutage):   true,
	}
	require.Equal(t, want, got, "the frontend's DAEMON_FORWARDED_STATES must equal exactly the daemon's forwardable BoardState set (BoardIdle excluded)")
}
