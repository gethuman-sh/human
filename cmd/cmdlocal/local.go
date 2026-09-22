// Package cmdlocal holds the commands only the local tracker can answer: the
// pipeline history it indexes as it is written.
package cmdlocal

import (
	"context"
	"encoding/json"
	"io"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// BuildLocalCommands returns the local-tracker-specific subcommands.
func BuildLocalCommands(deps cmdutil.Deps) []*cobra.Command {
	return []*cobra.Command{buildHistoryCmd(deps)}
}

// History is one ticket's recorded pipeline history: the markers as indexed
// and every event, oldest first.
type History struct {
	Key     string                  `json:"key"`
	Markers []tracker.IndexedMarker `json:"markers"`
	Events  []tracker.Event         `json:"events"`
}

func buildHistoryCmd(deps cmdutil.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "history KEY",
		Short: "Print a ticket's indexed markers and recorded events as JSON",
		Long: `Print what the local tracker recorded about a ticket: every [human:*] marker as it
was indexed when posted, and every change to the ticket as an event. This is the
ticket's history as the pipeline left it, readable without parsing comments.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, cleanup, err := cmdutil.ResolveProvider(cmd, "local", deps)
			if err != nil {
				return err
			}
			defer cleanup()
			return RunHistory(cmd.Context(), p, cmd.OutOrStdout(), args[0])
		},
	}
}

// RunHistory prints the history for key on a provider that indexes markers
// and records events. A provider that does neither is refused by name rather
// than answered with an empty history.
func RunHistory(ctx context.Context, p tracker.Provider, out io.Writer, key string) error {
	indexer, ok := p.(tracker.MarkerIndexer)
	if !ok {
		return errors.WithDetails("this tracker does not index markers", "key", key)
	}
	events, ok := p.(tracker.EventLister)
	if !ok {
		return errors.WithDetails("this tracker does not record events", "key", key)
	}
	markers, err := indexer.ListMarkers(ctx, key)
	if err != nil {
		return err
	}
	evs, err := events.ListEvents(ctx, key)
	if err != nil {
		return err
	}
	if markers == nil {
		markers = []tracker.IndexedMarker{}
	}
	if evs == nil {
		evs = []tracker.Event{}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(History{Key: key, Markers: markers, Events: evs})
}
