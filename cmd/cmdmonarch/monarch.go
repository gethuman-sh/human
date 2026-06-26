// Package cmdmonarch implements the standalone "monarch" binary: the team-level
// observability console. It runs a TCP ingest server that daemons stream
// identity-free events to, persists them to SQLite, and renders a live
// work-board + burn TUI.
package cmdmonarch

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/monarch"
)

// defaultAddr listens on a port distinct from the daemon (19285) and its
// chrome/proxy siblings so a monarch console and a daemon can coexist on a host.
const defaultAddr = "0.0.0.0:19290"

// BuildMonarchCmd creates the "monarch" command.
func BuildMonarchCmd() *cobra.Command {
	var addr string
	var headless bool
	cmd := &cobra.Command{
		Use:   "monarch",
		Short: "Team-level operations console for a swarm of human daemons",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMonarch(addr, headless)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", defaultAddr, "Listen address for daemon telemetry (host:port)")
	cmd.Flags().BoolVar(&headless, "headless", false, "Run the ingest server only (no TUI), hardened for systemd: readiness + watchdog + auto-restart on binary change")
	return cmd
}

func runMonarch(addr string, headless bool) error {
	store, err := monarch.NewStore(monarch.DefaultDBPath())
	if err != nil {
		return errors.WrapWithDetails(err, "open monarch store")
	}
	defer func() { _ = store.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Pruning is best-effort housekeeping; a failure must not block the console.
	_, _ = store.Prune(ctx)

	if headless {
		return runHeadless(ctx, addr, store)
	}
	return runConsole(ctx, addr, store, stop)
}

// runConsole runs the ingest server alongside an interactive TUI. The TUI owns
// the terminal, so logs are silenced to keep the alt-screen clean.
func runConsole(ctx context.Context, addr string, store *monarch.Store, stop context.CancelFunc) error {
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger().Level(zerolog.Disabled)
	startIngest(ctx, addr, store, logger)

	m := newModel(store)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	stop()
	return err
}

// runHeadless runs the ingest server with no TUI, hardened for systemd: it
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
