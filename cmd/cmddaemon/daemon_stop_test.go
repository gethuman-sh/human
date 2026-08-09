package cmddaemon

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shrinkStopGrace makes the pre-drain grace test-sized. The production value is
// five seconds; a test proving which branch runs does not need to spend them.
func shrinkStopGrace(t *testing.T, grace time.Duration) {
	t.Helper()
	orig := stopGrace
	stopGrace = grace
	t.Cleanup(func() { stopGrace = orig })
}

// livePID returns the pid of a process that stays alive until it is signalled,
// and is reaped the moment it dies — a zombie child still answers signal 0, so
// without the reaper a stopped process would keep looking alive to the very
// liveness check under test.
func livePID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	require.NoError(t, cmd.Start())
	reaped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})
	return cmd.Process.Pid
}

// A daemon that exits inside the grace period is the ordinary case: no waiting
// message, no error.
func TestAwaitDaemonExit_ExitsInGrace(t *testing.T) {
	shrinkStopGrace(t, 2*time.Second)
	pid := livePID(t)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = stopProcess(pid)
	}()

	var out bytes.Buffer
	require.NoError(t, awaitDaemonExit(&out, pid, 0, time.Minute, true, false))
	assert.Empty(t, out.String(), "an ordinary stop says nothing about waiting")
}

// The regression this exists for: a daemon draining in-flight work was reported
// as "did not exit within timeout" after five seconds, which reads as broken and
// is what sends an operator to a signal aimed by name — which also ends every
// other `human` process on the machine, the CLI half of the running deploy
// included. It must instead name what is being finished, and wait for it.
func TestAwaitDaemonExit_DrainingIsNamedAndWaitedOut(t *testing.T) {
	shrinkStopGrace(t, 200*time.Millisecond)
	pid := livePID(t)
	go func() {
		time.Sleep(600 * time.Millisecond) // outlives the grace, inside the wait
		_ = stopProcess(pid)
	}()

	var out bytes.Buffer
	require.NoError(t, awaitDaemonExit(&out, pid, 2, 5*time.Second, true, false))
	assert.Contains(t, out.String(), "2 in-flight operation(s)")
	assert.Contains(t, out.String(), "--force")
}

// Work that outlasts the wait is not a failure of the daemon: it exits on its
// own when the work is done, and the error has to say so rather than leave
// "kill it" as the only reading.
func TestAwaitDaemonExit_StillDrainingWhenTheWaitRunsOut(t *testing.T) {
	shrinkStopGrace(t, 200*time.Millisecond)
	var out bytes.Buffer
	err := awaitDaemonExit(&out, livePID(t), 1, 300*time.Millisecond, true, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exits on its own")
	assert.Contains(t, err.Error(), "--wait")
	assert.Contains(t, err.Error(), "--force")
}

// Nothing in flight and still alive past the grace is a genuinely stuck daemon:
// report it, and name the remedy that ends this daemon alone.
func TestAwaitDaemonExit_StuckWithNothingInFlight(t *testing.T) {
	shrinkStopGrace(t, 200*time.Millisecond)
	var out bytes.Buffer
	err := awaitDaemonExit(&out, livePID(t), 0, time.Hour, true, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no work in flight")
	assert.Empty(t, out.String(), "a stuck daemon is not reported as draining")
}

// An unreadable in-flight count must not be read as "it is busy, wait": unknown
// falls back to the stuck branch, which returns promptly.
func TestAwaitDaemonExit_UnknownCountDoesNotWait(t *testing.T) {
	shrinkStopGrace(t, 200*time.Millisecond)
	var out bytes.Buffer
	start := time.Now()
	err := awaitDaemonExit(&out, livePID(t), 0, time.Hour, false, false)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
}

// --force skips the drain entirely: it is the operator saying the work may be
// abandoned, so a surviving process is an error, not something to wait on.
func TestAwaitDaemonExit_ForceDoesNotDrain(t *testing.T) {
	shrinkStopGrace(t, 200*time.Millisecond)
	var out bytes.Buffer
	start := time.Now()
	err := awaitDaemonExit(&out, livePID(t), 3, time.Hour, true, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forced stop")
	assert.Less(t, time.Since(start), 5*time.Second)
}

// The stop command asks the daemon what it is finishing BEFORE signalling it,
// because a daemon that has begun shutting down has already closed the listener
// the question travels over — ask afterwards and the answer is always "cannot
// tell", which is the stuck branch.
func TestDaemonStop_ReadsInFlightBeforeSignalling(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pid := livePID(t)
	require.NoError(t, WritePidFile(pid))

	aliveWhenAsked := false
	orig := inFlightOps
	inFlightOps = func() (int, bool) {
		aliveWhenAsked = isProcessAlive(pid)
		return 0, true
	}
	t.Cleanup(func() { inFlightOps = orig })

	var out bytes.Buffer
	cmd := buildDaemonStopCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.NoError(t, cmd.Execute())
	assert.True(t, aliveWhenAsked, "the daemon must still be running when it is asked what it is finishing")
	assert.Contains(t, out.String(), "Daemon stopped")
}

// --force is a real flag, and its help draws the distinction that matters: it
// ends this daemon, not every process that happens to be called "human". --wait
// is the other half — the default is short so every scripted caller (the desktop
// close flow shells out to this command) still gets a prompt answer.
func TestDaemonStop_Flags(t *testing.T) {
	cmd := buildDaemonStopCmd()
	require.NotNil(t, cmd.Flags().Lookup("force"))
	wait := cmd.Flags().Lookup("wait")
	require.NotNil(t, wait)
	assert.Equal(t, stopDrainDefault.String(), wait.DefValue)
	assert.Less(t, stopDrainDefault, time.Minute, "the default wait must stay short enough for a scripted caller")
	assert.Contains(t, strings.ToLower(cmd.Long), "every other")
}
