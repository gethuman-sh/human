package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// blockedDeps builds a transition ready to start implementation, with the given
// dependency probe wired in.
func blockedDeps(t *testing.T, probe func(context.Context, string) ([]string, error)) (BoardTransitionDeps, *fakeCommenter, *fakeLauncher) {
	t.Helper()
	c := &fakeCommenter{comments: []tracker.Comment{cmt("[human:plan-ready]", time.Now().Add(-time.Minute))}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.DaemonID = "d1"
	deps.BlockedBy = probe
	return deps, c, l
}

func startImplementation(deps BoardTransitionDeps) error {
	return deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
}

// The whole point: work sequenced behind an open ticket does not start, and
// leaves nothing behind — no claim for another daemon to trip over, no marker
// that would have to be cleaned up when the blocker finishes.
func TestStartAgentStage_openBlockerRefusesLaunch(t *testing.T) {
	deps, c, l := blockedDeps(t, func(context.Context, string) ([]string, error) {
		return []string{"SC-2"}, nil
	})

	err := startImplementation(deps)

	require.Error(t, err, "the refusal must be reported: no other daemon can serve this stage either")
	assert.Contains(t, err.Error(), "waits for another ticket")
	assert.Zero(t, l.calls, "blocked work must not launch")
	assert.Empty(t, c.added, "blocked work must post no claim, started, or failed marker")
}

// A blocker that has already finished is not a blocker. The probe resolves real
// status, so a finished one simply never reaches the gate.
func TestStartAgentStage_finishedBlockerDoesNotRefuse(t *testing.T) {
	deps, _, l := blockedDeps(t, func(context.Context, string) ([]string, error) {
		return nil, nil
	})

	require.NoError(t, startImplementation(deps))
	assert.Equal(t, 1, l.calls)
}

// A tracker that cannot be reached must not hold the board. Refusing to start
// work because we could not read a dependency would turn a blip into a stall —
// the gate keeps two runs off the same code, it is not a liveness check.
func TestStartAgentStage_probeErrorDoesNotRefuse(t *testing.T) {
	deps, _, l := blockedDeps(t, func(context.Context, string) ([]string, error) {
		return nil, errors.New("tracker unreachable")
	})

	require.NoError(t, startImplementation(deps))
	assert.Equal(t, 1, l.calls, "an unreadable dependency starts the work rather than stalling it")
}

// Two tickets waiting for each other never resolve, so waiting is the one
// answer that cannot be right. Say so instead.
func TestStartAgentStage_cycleIsReported(t *testing.T) {
	deps, c, l := blockedDeps(t, func(_ context.Context, key string) ([]string, error) {
		if key == "SC-1" {
			return []string{"SC-2"}, nil
		}
		return []string{"SC-1"}, nil
	})

	err := startImplementation(deps)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait for each other")
	assert.Zero(t, l.calls)
	assert.Empty(t, c.added)
}

// A blocker whose own dependencies cannot be read is not evidence of a cycle;
// the ordinary refusal still applies and still names the blocker.
func TestStartAgentStage_unreadableBlockerFallsBackToPlainRefusal(t *testing.T) {
	deps, _, _ := blockedDeps(t, func(_ context.Context, key string) ([]string, error) {
		if key == "SC-1" {
			return []string{"SC-2"}, nil
		}
		return nil, errors.New("tracker unreachable")
	})

	err := startImplementation(deps)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "waits for another ticket")
}

// Review is the tail of work that already ran: its branch exists and can no
// longer collide with anything. Holding it would strand finished work behind a
// ticket it has already passed.
func TestStartAgentStage_laterStagesAreNotGated(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{
		cmt("[human:ready-for-review]\nengineering: HUM-9\nbranch: feat/x", time.Now().Add(-time.Minute)),
	}}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.DaemonID = "d1"
	deps.BlockedBy = func(context.Context, string) ([]string, error) { return []string{"SC-2"}, nil }

	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardVerification})

	require.NoError(t, err)
	assert.Equal(t, 1, l.calls, "work already underway continues past its blocker")
}

// An unwired probe leaves every existing board untouched.
func TestStartAgentStage_noProbeStartsAsBefore(t *testing.T) {
	deps, _, l := blockedDeps(t, nil)

	require.NoError(t, startImplementation(deps))
	assert.Equal(t, 1, l.calls)
}
