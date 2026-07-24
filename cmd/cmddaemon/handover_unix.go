//go:build !windows

package cmddaemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
)

// watchBinaryInterval is how often the self-restart watcher stats its own
// binary. Fast enough that a `make build` is picked up within a couple of
// seconds, cheap enough to run forever.
const watchBinaryInterval = 2 * time.Second

// handoverReadyTimeout bounds how long the parent waits for the re-exec'd child
// to report it is serving before giving up (and staying on the current binary).
const handoverReadyTimeout = 15 * time.Second

// handoverDrainTimeout caps how long the outgoing parent lingers for its
// in-flight proxied connections (agent egress) to finish before it exits.
// New connections already go to the child; this only protects streams that were
// mid-transfer at the moment of handover.
const handoverDrainTimeout = 30 * time.Second

// binStat is the change-detection fingerprint of the daemon binary. Size plus
// modification time is enough to notice a rebuild without hashing the file.
type binStat struct {
	size  int64
	mtime time.Time
}

func (a binStat) equal(b binStat) bool { return a.size == b.size && a.mtime.Equal(b.mtime) }

// watchAction is what a new stat reading calls for.
type watchAction int

const (
	actionWait     watchAction = iota // no change, or change not yet confirmed stable
	actionHandover                    // a new build has held stable for a debounce interval
)

// watchState debounces binary-change detection. A change must be observed
// unchanged twice in a row before it is trusted (so a half-written build mid-
// `go build` is never acted on). Kept separate from the polling loop so the
// decision logic is unit-testable without timers.
type watchState struct {
	baseline  binStat
	candidate binStat
	pending   bool
	haveBase  bool
}

// observe records a stat reading and reports whether the binary is a confirmed
// new build ready to hand over to.
func (w *watchState) observe(cur binStat) watchAction {
	if !w.haveBase {
		w.baseline, w.haveBase = cur, true
		return actionWait
	}
	if cur.equal(w.baseline) {
		w.pending = false
		return actionWait
	}
	if !w.pending || !cur.equal(w.candidate) {
		// First sighting of this new build — wait a tick to confirm it stopped
		// changing before trusting it.
		w.candidate, w.pending = cur, true
		return actionWait
	}
	return actionHandover
}

// reject abandons the current candidate (its build failed the sanity check or
// the handover errored), so it is not retried until the binary changes again.
func (w *watchState) reject(cur binStat) {
	w.baseline, w.pending, w.haveBase = cur, false, true
}

// handoverCoordinator watches the running daemon binary and, when it changes to
// a healthy new build, re-execs it while handing over the live listening
// sockets so no connected client is interrupted.
type handoverCoordinator struct {
	listeners   *listenerSet
	blockingOps func() int         // in-flight restart-blocking op count; a handover waits while > 0
	activeConns func() int64       // in-flight connections the parent drains before exiting
	retire      func()             // releases what the child cannot inherit (the PID-named relay socket)
	stop        context.CancelFunc // cancels the parent server context on handover
	handedOver  *atomic.Bool       // suppresses the parent's file cleanup once the child owns them
	logger      zerolog.Logger
	execPath    string

	// Injectable seams for tests.
	interval     time.Duration
	drainTimeout time.Duration
	statOf       func(path string) (binStat, error)
	sanity       func(ctx context.Context, path string) error
	reexec       func(ctx context.Context, c *handoverCoordinator) error
}

// maybeWatchBinary starts the self-restart watcher unless it is disabled or the
// binary path cannot be resolved. Safe to call unconditionally.
func maybeWatchBinary(ctx context.Context, ls *listenerSet, srv *daemon.Server, hooks handoverHooks, stop context.CancelFunc, handedOver *atomic.Bool, logger zerolog.Logger) {
	if watchBinaryDisabled() {
		logger.Info().Msg("daemon self-restart on binary change disabled (HUMAN_DAEMON_WATCH_BINARY)")
		return
	}
	execPath, err := os.Executable()
	if err != nil {
		logger.Warn().Err(err).Msg("cannot resolve daemon binary path, self-restart watcher off")
		return
	}
	c := &handoverCoordinator{
		listeners:    ls,
		blockingOps:  srv.BlockingOps,
		activeConns:  hooks.activeConns,
		retire:       hooks.retire,
		stop:         stop,
		handedOver:   handedOver,
		logger:       logger,
		execPath:     execPath,
		interval:     watchBinaryInterval,
		drainTimeout: handoverDrainTimeout,
		statOf:       defaultBinStat,
		sanity:       defaultSanityCheck,
		reexec:       reexecChild,
	}
	go c.watch(ctx)
}

func defaultBinStat(path string) (binStat, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return binStat{}, err
	}
	return binStat{size: fi.Size(), mtime: fi.ModTime()}, nil
}

// defaultSanityCheck proves the new binary actually loads and runs before the
// daemon commits to re-exec'ing it — a half-written or broken build fails here
// and the current daemon keeps serving. `--version` is fast and side-effect free.
func defaultSanityCheck(ctx context.Context, path string) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, path, "--version").Run() // #nosec G204 -- path is our own os.Executable()
}

// watch polls the daemon binary and hands over to a rebuilt one. A change must
// be stable for one interval (debounce) before it acts, so a half-written build
// mid-`go build` is never exec'd. A postponed handover (blocking ops in flight)
// is retried on the next tick; a binary that fails the sanity check is ignored
// until it changes again.
func (c *handoverCoordinator) watch(ctx context.Context) {
	var st watchState
	if base, err := c.statOf(c.execPath); err == nil {
		st.observe(base) // seed the baseline from the running binary
	} else {
		c.logger.Warn().Err(err).Msg("self-restart watcher: initial stat failed")
	}

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		cur, err := c.statOf(c.execPath)
		if err != nil {
			continue // binary momentarily absent (mid-rename); re-check next tick
		}
		if st.observe(cur) != actionHandover {
			continue
		}

		// Stable new build. Vet it, then hand over.
		if err := c.sanity(ctx, c.execPath); err != nil {
			c.logger.Warn().Err(err).Msg("self-restart: new binary failed sanity check, staying on current")
			st.reject(cur) // don't retry until it changes again
			continue
		}
		if n := c.blockingOps(); n > 0 {
			c.logger.Info().Int("blocking_ops", n).Msg("self-restart postponed: work in flight, retrying")
			continue // state unchanged, so the next tick retries without a new change
		}
		if err := c.reexec(ctx, c); err != nil {
			c.logger.Error().Err(err).Msg("self-restart handover failed, staying on current binary")
			st.reject(cur) // avoid a tight retry loop on the same build
			continue
		}
		return // handed over; the parent server context is cancelled and this process will exit
	}
}

// reexecChild launches the new binary, hands it the live listening sockets, and
// waits for it to report ready. On success it cancels the parent server context
// (so the parent drains and exits) after marking the handover so the parent
// never deletes the pidfile/daemon.json the child just wrote. On any failure it
// returns without touching the parent, which keeps serving.
func reexecChild(ctx context.Context, c *handoverCoordinator) error {
	files, err := c.listeners.files()
	if err != nil {
		return err
	}

	readyR, readyW, err := os.Pipe()
	if err != nil {
		closeAllFiles(files)
		return errors.WrapWithDetails(err, "creating handover readiness pipe")
	}

	// ExtraFiles map to fds 3..; the readiness pipe follows the three listeners.
	extra := append(files, readyW) // #nosec G601 -- files is not retained after this call
	readyFD := 3 + len(files)

	// Re-exec of our own binary (os.Executable) with our own argv, forwarded
	// verbatim so the successor serves the same flags. Neither is attacker
	// reachable: anyone who could choose these already controls this process.
	cmd := exec.Command(c.execPath, os.Args[1:]...) // #nosec G204,G702 -- own binary, own argv
	cmd.Env = append(os.Environ(),
		daemonChildEnv+"=1",
		envInheritListeners+"="+handoverListenerSpec,
		fmt.Sprintf("%s=%d", envHandoverReadyFD, readyFD),
	)
	cmd.Stdout = os.Stdout // inherit the daemon log file the parent writes to
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = extra
	cmd.SysProcAttr = detachSysProcAttr()

	if err := cmd.Start(); err != nil {
		closeAllFiles(files)
		_ = readyW.Close()
		_ = readyR.Close()
		return errors.WrapWithDetails(err, "starting handover child")
	}
	// The child inherited its own copies of every fd; release the parent's.
	closeAllFiles(files)
	_ = readyW.Close()
	defer func() { _ = readyR.Close() }()

	if err := waitHandoverReady(ctx, readyR, handoverReadyTimeout); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	_ = cmd.Process.Release()

	// Commit: from here the child owns the pidfile, daemon.json and stats files,
	// so the parent's cleanup defers must not delete them.
	c.handedOver.Store(true)
	c.logger.Info().Int("child_pid", cmd.Process.Pid).Msg("daemon self-restart: child ready, handing over")

	// Release what the child could not inherit before anything else discovers
	// it: the chrome relay's socket is named after this process id and clients
	// pick one by globbing, so leaving it in place lets a client attach to the
	// daemon that is about to exit.
	if c.retire != nil {
		c.retire()
	}

	// New connections already reach the child; let any in-flight streams (agent
	// egress or a chrome session that was mid-transfer) finish before the parent
	// exits, so nothing live is cut off. Bounded so a stuck stream can't wedge
	// the handover forever.
	c.drainInflight(ctx)

	c.stop()
	return nil
}

// drainInflight blocks until the parent has no in-flight proxied connections
// left, or the drain deadline elapses. Daemon command handlers are drained
// separately by the server's own graceful shutdown (s.wg), and long-running
// board/deploy work never reaches here because a handover is postponed while it
// is in flight — so this only waits on proxied agent egress.
func (c *handoverCoordinator) drainInflight(ctx context.Context) {
	if c.activeConns == nil {
		return
	}
	deadline := time.Now().Add(c.drainTimeout)
	for {
		n := c.activeConns()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			c.logger.Warn().Int64("active_conns", n).Msg("handover drain timed out; remaining in-flight connections will be dropped")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// waitHandoverReady blocks until the child signals it is serving, the child
// dies before signaling (readiness pipe hits EOF), or the timeout elapses.
func waitHandoverReady(ctx context.Context, r *os.File, timeout time.Duration) error {
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, 8)
		n, err := r.Read(buf)
		if n > 0 {
			done <- result{ok: true}
			return
		}
		done <- result{err: err}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(timeout):
		return errors.WithDetails("handover child did not report ready in time", "timeout", timeout.String())
	case res := <-done:
		if res.ok {
			return nil
		}
		if res.err == nil || res.err == io.EOF {
			return errors.WithDetails("handover child exited before reporting ready")
		}
		return errors.WrapWithDetails(res.err, "reading handover readiness")
	}
}
