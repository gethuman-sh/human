// Package cmdmonarch implements the standalone "monarch" binary: the team-level
// observability console. It runs a TCP ingest server that daemons stream
// identity-free events to, persists them to SQLite, and serves a read-only web
// dashboard (work board + burn + capacity) that auto-refreshes in a browser.
package cmdmonarch

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/monarch"
)

// defaultAddr listens on a port distinct from the daemon (19285) and its
// chrome/proxy siblings so a monarch console and a daemon can coexist on a host.
const defaultAddr = "0.0.0.0:19290"

// defaultWebAddr serves the dashboard on a second port, distinct from the ingest
// port (19290) and the daemon (19285), so both servers share one process and one
// store without colliding.
const defaultWebAddr = "0.0.0.0:19291"

// BuildMonarchCmd creates the "monarch" command.
func BuildMonarchCmd() *cobra.Command {
	var addr string
	var webAddr string
	var headless bool
	cmd := &cobra.Command{
		Use:   "monarch",
		Short: "Team-level operations console for a swarm of human daemons",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMonarch(addr, webAddr, headless)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", defaultAddr, "Listen address for daemon telemetry (host:port)")
	cmd.Flags().StringVar(&webAddr, "web-addr", defaultWebAddr, "Listen address for the web dashboard (host:port)")
	cmd.Flags().BoolVar(&headless, "headless", false, "Run the ingest server only (no web UI), hardened for systemd: readiness + watchdog + auto-restart on binary change")
	return cmd
}

func runMonarch(addr, webAddr string, headless bool) error {
	store, err := monarch.NewStore(monarch.DefaultDBPath())
	if err != nil {
		return errors.WrapWithDetails(err, "open monarch store")
	}
	defer func() { _ = store.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Pruning is best-effort housekeeping; a failure must not block the console.
	_, _ = store.Prune(ctx)

	// Loudly flag the absence of auth on startup: monarch binds its ingest and
	// web ports with no authentication, so anyone who can reach them sees the
	// swarm telemetry. Run only on a trusted/private network.
	warnLogger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	warnLogger.Warn().Msg("⚠️  THERE IS NO AUTH — monarch is unauthenticated; run only on a trusted/private network")

	if headless {
		return runHeadless(ctx, addr, store)
	}
	return runConsole(ctx, addr, webAddr, store)
}

// runConsole runs the ingest server alongside the read-only web dashboard,
// sharing one store. Both servers run in the background and ingest/serve errors
// are non-fatal, so the console stays up; it blocks until a signal cancels ctx.
func runConsole(ctx context.Context, addr, webAddr string, store *monarch.Store) error {
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	startIngest(ctx, addr, store, logger)
	startWeb(ctx, webAddr, store, logger)
	logger.Info().Str("ingest", addr).Str("web", webAddr).Msg("monarch console ready")

	<-ctx.Done()
	return nil
}

// runHeadless runs the ingest server with no web UI, hardened for systemd: it
// announces readiness, answers the watchdog, and exits when its own binary is
// replaced so systemd restarts it on the new release. Blocks until ctx is done.
func runHeadless(ctx context.Context, addr string, store *monarch.Store) error {
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
	startIngest(ctx, addr, store, logger)

	monarch.WatchBinaryAndExit(logger)
	monarch.StartSystemdWatchdog(ctx, logger)
	monarch.NotifySystemdReady(logger)
	logger.Info().Str("addr", addr).Msg("monarch ingest server ready (headless)")

	<-ctx.Done()
	return nil
}

// startIngest launches the TCP ingest server in the background. Ingest errors
// are non-fatal — the console/store stays available even if the listener dies.
func startIngest(ctx context.Context, addr string, store *monarch.Store, logger zerolog.Logger) {
	srv := &monarch.Server{Addr: addr, Store: store, Logger: logger}
	go func() {
		if serveErr := srv.ListenAndServe(ctx); serveErr != nil {
			logger.Error().Err(serveErr).Msg("monarch ingest server failed")
		}
	}()
}

// startWeb launches the read-only web dashboard in the background. A web failure
// is non-fatal — telemetry ingest continues even if the dashboard cannot bind.
func startWeb(ctx context.Context, addr string, store *monarch.Store, logger zerolog.Logger) {
	web := &monarch.WebServer{Addr: addr, Store: store, Logger: logger}
	go func() {
		if serveErr := web.ListenAndServe(ctx); serveErr != nil {
			logger.Error().Err(serveErr).Msg("monarch web server failed")
		}
	}()
}
