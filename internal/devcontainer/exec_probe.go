package devcontainer

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// execSettleTimeout bounds how long a finished exec may take to publish its exit
// code. The stream EOF already means the process ended, so this is only the gap
// between that and the daemon's bookkeeping catching up; a probe still running
// when it expires is reported as unaskable rather than answered from a field
// that has no meaning yet.
//
// execSettlePoll is how often that is re-checked. Both are variables so tests
// can drive the settle loop without real waiting.
var (
	execSettleTimeout = 5 * time.Second
	execSettlePoll    = 20 * time.Millisecond
)

// ProcessRunning reports whether a process with exactly this name is running
// inside the container, by exit code of `pgrep -x`.
//
// An error means the question could not be asked — never that the process is
// absent. Callers act on absence (reaping an agent, tearing down a container),
// so a probe that cannot answer must say so and let them decide, rather than
// hand back a false negative that reads as death.
func ProcessRunning(ctx context.Context, docker DockerClient, containerID, process string) (bool, error) {
	if docker == nil {
		return false, errors.WithDetails("no docker client to probe with", "process", process)
	}
	code, err := execExitCode(ctx, docker, containerID, []string{"pgrep", "-x", process})
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

// execExitCode runs cmd to completion in the container and returns its exit
// code.
//
// Attaching stdout and stderr is what makes the code readable at all: the
// attachment is the only signal that says the process ended, because its stream
// EOFs when it does. An exec created without them yields an empty stream that
// EOFs immediately, and the inspect below then reads ExitCode while the process
// is still Running — where it is 0, the zero value, and indistinguishable from
// success (SC-4281).
func execExitCode(ctx context.Context, docker DockerClient, containerID string, cmd []string) (int, error) {
	execID, err := docker.ExecCreate(ctx, containerID, cmd, ExecOptions{AttachStdout: true, AttachStderr: true})
	if err != nil {
		return 0, errors.WrapWithDetails(err, "creating exec", "container", containerID, "cmd", fmt.Sprint(cmd))
	}
	attach, err := docker.ExecAttach(ctx, execID)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "attaching to exec", "container", containerID, "cmd", fmt.Sprint(cmd))
	}
	// A stalled stream must not park the caller — the zombie sweep drains inline
	// on its single goroutine — so the watchdog closes the attachment when ctx
	// expires, unblocking the drain (SC-427).
	stop := closeExecOnContextDone(ctx, attach)
	_, _ = StdCopy(io.Discard, io.Discard, attach.Reader)
	stop()
	_ = attach.Close()

	return settledExitCode(ctx, docker, execID, cmd)
}

// settledExitCode reads the exec's exit code once the exec has actually
// finished, polling until it has. A still-Running exec carries no exit code, so
// waiting is the only way to get an answer and a timeout is the honest report
// when it never arrives.
func settledExitCode(ctx context.Context, docker DockerClient, execID string, cmd []string) (int, error) {
	deadline := time.Now().Add(execSettleTimeout)
	for {
		inspect, err := docker.ExecInspect(ctx, execID)
		if err != nil {
			return 0, errors.WrapWithDetails(err, "inspecting exec result", "cmd", fmt.Sprint(cmd))
		}
		if !inspect.Running {
			return inspect.ExitCode, nil
		}
		if !time.Now().Before(deadline) {
			return 0, errors.WithDetails("exec did not finish in time to report an exit code",
				"cmd", fmt.Sprint(cmd), "waited", execSettleTimeout.String())
		}
		select {
		case <-ctx.Done():
			return 0, errors.WrapWithDetails(ctx.Err(), "waiting for exec to finish", "cmd", fmt.Sprint(cmd))
		case <-time.After(execSettlePoll):
		}
	}
}

// closeExecOnContextDone starts a watchdog that closes the exec attachment when
// ctx is cancelled, unblocking a drain parked on a stalled stream (closing the
// attachment closes its underlying conn). It returns a stop func the caller
// invokes once the drain has finished, tearing the watchdog down.
func closeExecOnContextDone(ctx context.Context, attach ExecAttachResponse) (stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = attach.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}
