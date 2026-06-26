package monarch

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// silentLogger keeps test output clean while still exercising the log calls.
func silentLogger() zerolog.Logger {
	return zerolog.New(zerolog.Nop()).Level(zerolog.Disabled)
}

// Outside systemd (NOTIFY_SOCKET unset in the test env) readiness is a no-op and
// must not panic or block.
func TestNotifySystemdReady_noSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	NotifySystemdReady(silentLogger())
}

// With no watchdog configured the watchdog loop must not start; the call returns
// immediately and cancelling ctx is a no-op.
func TestStartSystemdWatchdog_disabled(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "")
	t.Setenv("WATCHDOG_PID", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartSystemdWatchdog(ctx, silentLogger())
	// Nothing to assert beyond "does not hang/panic"; give any stray goroutine a
	// moment to misbehave before the test ends.
	time.Sleep(10 * time.Millisecond)
}

// WatchBinaryAndExit sets up the fsnotify watcher on the running binary and
// returns; with no change to the executable it must not exit the process.
func TestWatchBinaryAndExit_setupNoExit(t *testing.T) {
	WatchBinaryAndExit(silentLogger())
	// If the watcher erroneously fired os.Exit on setup, the test binary would
	// die here; reaching this point with the process alive is the assertion.
	time.Sleep(20 * time.Millisecond)
}
