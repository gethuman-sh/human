// Package cmdmonarch implements "human monarch": the team-level observability
// console. It runs a TCP ingest server that daemons stream identity-free events
// to, persists them to SQLite, and renders a live work-board + burn TUI.
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
	cmd := &cobra.Command{
		Use:     "monarch",
		Short:   "Team-level operations console for a swarm of human daemons",
		GroupID: "utility",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMonarch(addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", defaultAddr, "Listen address for daemon telemetry (host:port)")
	return cmd
}

func runMonarch(addr string) error {
	store, err := monarch.NewStore(monarch.DefaultDBPath())
	if err != nil {
		return errors.WrapWithDetails(err, "open monarch store")
	}
	defer func() { _ = store.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Pruning is best-effort housekeeping; a failure must not block the console.
	_, _ = store.Prune(ctx)

	// The ingest server runs while the TUI owns the terminal. Logs are written to
	// stderr but the TUI alt-screen hides them; ingest errors are non-fatal.
	logger := zerolog.New(os.Stderr).With().Timestamp().Logger().Level(zerolog.Disabled)
	srv := &monarch.Server{Addr: addr, Store: store, Logger: logger}
	go func() {
		if serveErr := srv.ListenAndServe(ctx); serveErr != nil {
			logger.Error().Err(serveErr).Msg("monarch ingest server failed")
		}
	}()

	m := newModel(store)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	stop()
	return err
}
