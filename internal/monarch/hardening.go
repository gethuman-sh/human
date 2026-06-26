package monarch

import (
	"context"
	"os"
	"time"

	systemd "github.com/coreos/go-systemd/v22/daemon"
	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"
)

// WatchBinaryAndExit exits the process when the running executable is replaced
// on disk, so a process manager (systemd) restarts it on the new binary. This
// is the zero-touch-deploy hook: drop in a new monarch binary, the running
// server exits, systemd brings it back up on the new release. It is best-effort
// — if the watcher cannot be created it logs and returns, leaving the server
// running rather than refusing to start.
func WatchBinaryAndExit(logger zerolog.Logger) {
	path, err := os.Executable()
	if (err != nil || path == "") && len(os.Args) > 0 {
		// Fall back to argv[0]; on most systems os.Executable resolves a stable
		// absolute path, but never block startup if it does not.
		path = os.Args[0]
	}
	if path == "" {
		logger.Error().Msg("monarch: cannot determine executable path to watch")
		return
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Error().Err(err).Msg("monarch: cannot watch binary for changes")
		return
	}
	if addErr := watcher.Add(path); addErr != nil {
		logger.Error().Err(addErr).Str("path", path).Msg("monarch: cannot watch binary path")
		_ = watcher.Close()
		return
	}
	go func() {
		defer func() { _ = watcher.Close() }()
		for {
			select {
			case _, ok := <-watcher.Events:
				if !ok {
					return
				}
				// Any event on the executable (write, rename, remove) means the
				// release changed; exit so systemd restarts us on the new binary.
				logger.Info().Str("path", path).Msg("monarch: executable changed, exiting for restart")
				os.Exit(0)
			case watchErr, ok := <-watcher.Errors:
				if !ok {
					return
				}
				logger.Error().Err(watchErr).Msg("monarch: binary watcher error")
			}
		}
	}()
}

// NotifySystemdReady tells systemd (Type=notify) the service is up and ready.
// It is a no-op when not run under systemd (NOTIFY_SOCKET unset).
func NotifySystemdReady(logger zerolog.Logger) {
	if _, err := systemd.SdNotify(false, systemd.SdNotifyReady); err != nil {
		logger.Debug().Err(err).Msg("monarch: sd_notify READY failed")
	}
}

// StartSystemdWatchdog pings the systemd watchdog at half the configured
// interval so the service is not killed for appearing hung. It is a no-op when
// the watchdog is not enabled, and stops when ctx is done.
func StartSystemdWatchdog(ctx context.Context, logger zerolog.Logger) {
	interval, err := systemd.SdWatchdogEnabled(false)
	if err != nil || interval == 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval / 2)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, notifyErr := systemd.SdNotify(false, systemd.SdNotifyWatchdog); notifyErr != nil {
					logger.Debug().Err(notifyErr).Msg("monarch: sd_notify WATCHDOG failed")
				}
			}
		}
	}()
}
