package daemon

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDaemonFlowStatesMatchFrontend is the daemon half of the flow drift lock
// (SC-3577), the twin of TestDaemonForwardedStatesMatchFrontend: the frontend's
// FLOW_STATES list must name exactly the daemon's BoardFlow* constants. Adding a
// flow state on either side without teaching the other fails this test or its
// frontend twin.
func TestDaemonFlowStatesMatchFrontend(t *testing.T) {
	data, err := os.ReadFile("../../desktop/frontend/src/board-states.ts")
	require.NoError(t, err, "board-states.ts must exist and be readable from internal/daemon")

	m := regexp.MustCompile(`FLOW_STATES\s*=\s*\[([^\]]*)\]`).FindSubmatch(data)
	require.NotNil(t, m, "board-states.ts must define FLOW_STATES = [...]")

	literals := regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1)
	got := make(map[string]bool, len(literals))
	for _, lit := range literals {
		got[string(lit[1])] = true
	}

	want := map[string]bool{
		BoardFlowFlowing: true,
		BoardFlowIdle:    true,
		BoardFlowStalled: true,
		BoardFlowUnknown: true,
	}
	require.Equal(t, want, got, "the frontend's FLOW_STATES must equal exactly the daemon's BoardFlow* set")
}
