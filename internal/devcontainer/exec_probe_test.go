package devcontainer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settleDocker models the one behaviour the real engine has and the plain mock
// does not: an exec that is still Running for its first inspections, carrying no
// exit code, before it settles on one.
type settleDocker struct {
	*mockDockerClient
	runningFor  int // inspections that report Running before the exec settles
	settledExit int
	inspects    int
	inspectErr  error
}

func (s *settleDocker) ExecInspect(_ context.Context, _ string) (ExecInspectResponse, error) {
	if s.inspectErr != nil {
		return ExecInspectResponse{}, s.inspectErr
	}
	s.inspects++
	if s.inspects <= s.runningFor {
		// A running exec's ExitCode field is the zero value, which is exactly
		// what makes reading it early indistinguishable from success.
		return ExecInspectResponse{Running: true, ExitCode: 0}, nil
	}
	return ExecInspectResponse{Running: false, ExitCode: s.settledExit}, nil
}

func newSettleDocker(runningFor, settledExit int) *settleDocker {
	return &settleDocker{mockDockerClient: &mockDockerClient{}, runningFor: runningFor, settledExit: settledExit}
}

// withFastSettle shrinks the settle budget so a test drives the loop without
// waiting in real time. Restores the package defaults on cleanup.
func withFastSettle(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldTimeout, oldPoll := execSettleTimeout, execSettlePoll
	execSettleTimeout, execSettlePoll = timeout, time.Millisecond
	t.Cleanup(func() { execSettleTimeout, execSettlePoll = oldTimeout, oldPoll })
}

// The exit code is only readable because the stream EOFs when the process ends,
// and it only EOFs if the output is attached. An unattached exec is the SC-4281
// defect itself, so the flags are asserted rather than assumed.
func TestProcessRunning_AttachesOutputSoTheDrainSynchronises(t *testing.T) {
	withFastSettle(t, time.Second)
	docker := newSettleDocker(0, 0)

	_, err := ProcessRunning(context.Background(), docker, "cid", "claude")

	require.NoError(t, err)
	require.Len(t, docker.execCalls, 1)
	assert.True(t, docker.execCalls[0].Opts.AttachStdout, "stdout must be attached")
	assert.True(t, docker.execCalls[0].Opts.AttachStderr, "stderr must be attached")
	assert.Equal(t, []string{"pgrep", "-x", "claude"}, docker.execCalls[0].Cmd)
}

func TestProcessRunning_ExitCodeDecides(t *testing.T) {
	tests := []struct {
		name string
		exit int
		want bool
	}{
		{"pgrep found the process", 0, true},
		{"pgrep found nothing", 1, false},
		{"pgrep failed", 2, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withFastSettle(t, time.Second)
			running, err := ProcessRunning(context.Background(), newSettleDocker(0, tc.exit), "cid", "claude")
			require.NoError(t, err)
			assert.Equal(t, tc.want, running)
		})
	}
}

// The regression: a still-running exec reports ExitCode 0, which read as an
// answer means "the process is alive" for every container ever probed — the
// zombie sweep then never reaps a dead agent and the board spins forever.
func TestProcessRunning_WaitsForTheExecInsteadOfReadingAZeroExitCode(t *testing.T) {
	withFastSettle(t, time.Second)
	docker := newSettleDocker(3, 1)

	running, err := ProcessRunning(context.Background(), docker, "cid", "claude")

	require.NoError(t, err)
	assert.False(t, running, "the settled exit code 1 is the answer, not the running exec's 0")
	assert.Equal(t, 4, docker.inspects, "should have polled until the exec settled")
}

// An exec that never finishes has no exit code to report. Answering "not
// running" would be a false death — the caller reaps live work on it — so the
// probe reports that it could not ask.
func TestProcessRunning_UnsettledExecIsAnErrorNotAbsence(t *testing.T) {
	withFastSettle(t, 10*time.Millisecond)
	docker := newSettleDocker(1_000_000, 0)

	running, err := ProcessRunning(context.Background(), docker, "cid", "claude")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exec did not finish")
	assert.False(t, running)
}

func TestProcessRunning_CancelledContextIsAnError(t *testing.T) {
	withFastSettle(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ProcessRunning(ctx, newSettleDocker(1_000_000, 0), "cid", "claude")

	require.Error(t, err)
}

func TestProcessRunning_ProbeFailuresSurfaceAsErrors(t *testing.T) {
	withFastSettle(t, time.Second)
	boom := errors.New("docker is unwell")

	t.Run("exec cannot be created", func(t *testing.T) {
		docker := newSettleDocker(0, 0)
		docker.execErr = boom
		_, err := ProcessRunning(context.Background(), docker, "cid", "claude")
		require.ErrorIs(t, err, boom)
	})

	t.Run("exec cannot be inspected", func(t *testing.T) {
		docker := newSettleDocker(0, 0)
		docker.inspectErr = boom
		_, err := ProcessRunning(context.Background(), docker, "cid", "claude")
		require.ErrorIs(t, err, boom)
	})

	t.Run("no client at all", func(t *testing.T) {
		_, err := ProcessRunning(context.Background(), nil, "cid", "claude")
		require.Error(t, err)
	})
}
