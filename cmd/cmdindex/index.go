package cmdindex

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/index"
	"github.com/gethuman-sh/human/internal/tracker"
)

// IndexDeps holds injectable dependencies for index commands.
type IndexDeps struct {
	LoadInstances       func(dir string) ([]tracker.Instance, error)
	LoadNotionInstances func(dir string) ([]index.NotionInstance, error)
	DBPath              func() string
	NewStore            func(dbPath string) (index.Store, error)
}

// DefaultIndexDeps returns production dependencies.
func DefaultIndexDeps() IndexDeps {
	return IndexDeps{
		LoadInstances:       cmdutil.LoadAllInstances,
		LoadNotionInstances: cmdutil.LoadNotionIndexInstances,
		DBPath:              index.DefaultDBPath,
		NewStore: func(dbPath string) (index.Store, error) {
			return index.NewSQLiteStore(dbPath)
		},
	}
}

// BuildIndexCmd creates the "index" command.
func BuildIndexCmd(deps IndexDeps) *cobra.Command {
	var (
		status bool
		source string
		full   bool
	)

	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build search index from configured trackers and Notion",
		Long:  "Sync issues from all configured tracker instances and Notion workspaces into a local SQLite index for fast full-text search.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if status {
				return RunIndexStatus(cmd.Context(), cmd.OutOrStdout(), deps)
			}
			return RunIndex(cmd.Context(), cmd.OutOrStdout(), source, full, deps)
		},
	}

	cmd.Flags().BoolVar(&status, "status", false, "Show index statistics instead of syncing")
	cmd.Flags().StringVar(&source, "source", "", "Only sync instances of this tracker kind (e.g. jira, linear)")
	cmd.Flags().BoolVar(&full, "full", false, "Force full sync (include closed/done issues, prune stale entries)")

	return cmd
}

// BuildSearchCmd creates the "search" command.
func BuildSearchCmd(deps IndexDeps) *cobra.Command {
	var (
		limit    int
		source   string
		jsonOut  bool
		tableOut bool
	)

	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search the local issue index",
		Long:  "Full-text search across all indexed tracker issues and Notion pages. Run 'human index' first to build the index.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunSearch(cmd.Context(), cmd.OutOrStdout(), args[0], limit, source, jsonOut, tableOut, deps)
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of results")
	cmd.Flags().StringVar(&source, "source", "", "Filter results by kind (e.g. notion, jira)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&tableOut, "table", false, "Output as table")

	return cmd
}

// RunIndex loads instances, opens the store, and syncs.
func RunIndex(ctx context.Context, out io.Writer, source string, full bool, deps IndexDeps) error {
	instances, err := deps.LoadInstances(".")
	if err != nil {
		return err
	}

	cmdutil.WarnSkippedTrackers(out, ".", instances)

	if source != "" {
		var filtered []tracker.Instance
		for _, inst := range instances {
			if inst.Kind == source {
				filtered = append(filtered, inst)
			}
		}
		instances = filtered
	}

	// Check if we have anything to sync.
	hasNotion := (source == "" || source == "notion") && deps.LoadNotionInstances != nil

	if len(instances) == 0 && !hasNotion {
		_, _ = fmt.Fprintln(out, "No trackers connected. Run 'human init' or set credentials (see above).")
		return nil
	}

	store, err := deps.NewStore(deps.DBPath())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	if len(instances) > 0 {
		result, err := index.Sync(ctx, store, instances, full, out)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "Trackers: %d indexed, %d pruned, %d errors\n", result.Indexed, result.Pruned, result.Errors)
	}

	if hasNotion {
		syncNotion(ctx, out, store, deps)
	}

	_, _ = fmt.Fprintln(out, "\nDone.")
	return nil
}

// syncNotion loads and syncs Notion instances.
func syncNotion(ctx context.Context, out io.Writer, store index.Store, deps IndexDeps) {
	notionInstances, err := deps.LoadNotionInstances(".")
	if err != nil {
		_, _ = fmt.Fprintf(out, "Warning: failed to load Notion instances: %v\n", err)
		return
	}
	if len(notionInstances) == 0 {
		return
	}
	notionResult, err := index.SyncNotion(ctx, store, notionInstances, out)
	if err != nil {
		_, _ = fmt.Fprintf(out, "Error syncing Notion: %v\n", err)
		return
	}
	_, _ = fmt.Fprintf(out, "Notion: %d pages, %d databases indexed, %d pruned, %d errors\n",
		notionResult.Pages, notionResult.Databases, notionResult.Pruned, notionResult.Errors)
}

// RunSearch opens the store and searches.
func RunSearch(ctx context.Context, out io.Writer, query string, limit int, source string, jsonOut, tableOut bool, deps IndexDeps) error {
	store, err := deps.NewStore(deps.DBPath())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	// Push the source/kind filter into the store so the LIMIT is
	// applied AFTER the kind restriction. Filtering client-side after a
	// LIMIT can hide matching results when the top-ranked hits belong
	// to a different kind.
	var entries []index.Entry
	if source != "" {
		entries, err = store.SearchWithKind(ctx, query, source, limit)
	} else {
		entries, err = store.Search(ctx, query, limit)
	}
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		_, _ = fmt.Fprintln(out, "No results found. Run 'human index' to build or update the index.")
		return nil
	}

	if jsonOut {
		return cmdutil.PrintJSON(out, entries)
	}

	if tableOut {
		return printSearchTable(out, entries)
	}

	return printSearchDefault(out, entries)
}

// RunIndexStatus shows index statistics.
func RunIndexStatus(ctx context.Context, out io.Writer, deps IndexDeps) error {
	store, err := deps.NewStore(deps.DBPath())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	stats, err := store.Stats(ctx)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "Total entries: %d\n", stats.TotalEntries)
	if !stats.LastIndexedAt.IsZero() {
		_, _ = fmt.Fprintf(out, "Last indexed:  %s\n", stats.LastIndexedAt.Format("2006-01-02 15:04:05"))
	}

	if len(stats.ByKind) > 0 {
		_, _ = fmt.Fprintln(out, "\nBy tracker:")
		for kind, count := range stats.ByKind {
			_, _ = fmt.Fprintf(out, "  %-12s %d\n", kind, count)
		}
	}

	if len(stats.BySource) > 0 {
		_, _ = fmt.Fprintln(out, "\nBy source:")
		for source, count := range stats.BySource {
			_, _ = fmt.Fprintf(out, "  %-12s %d\n", source, count)
		}
	}

	return nil
}

// printSearchDefault prints results in the agent-friendly default format.
func printSearchDefault(out io.Writer, entries []index.Entry) error {
	for _, e := range entries {
		if e.Kind == "notion" {
			_, _ = fmt.Fprintln(out, e.Title)
			_, _ = fmt.Fprintf(out, "  notion \u00b7 %s \u00b7 %s\n", e.Source, e.Status)
			if e.Status == "database" {
				_, _ = fmt.Fprintf(out, "  \u2192 human notion database query %s\n", e.Key)
			} else {
				_, _ = fmt.Fprintf(out, "  \u2192 human notion page get %s\n", e.Key)
			}
			continue
		}
		_, _ = fmt.Fprintf(out, "%s: %s\n", e.Key, e.Title)
		meta := fmt.Sprintf("  %s", e.Kind)
		if e.Source != "" {
			meta += " \u00b7 " + e.Source
		}
		if e.Status != "" {
			meta += " \u00b7 " + e.Status
		}
		if e.Assignee != "" {
			meta += " \u00b7 @" + e.Assignee
		}
		_, _ = fmt.Fprintln(out, meta)
		if e.Source != "" {
			_, _ = fmt.Fprintf(out, "  \u2192 human get %s --tracker=%s\n", e.Key, e.Source)
		} else {
			_, _ = fmt.Fprintf(out, "  \u2192 human get %s\n", e.Key)
		}
	}
	return nil
}

// printSearchTable prints results as a formatted table.
func printSearchTable(out io.Writer, entries []index.Entry) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "KEY\tTITLE\tKIND\tSOURCE\tSTATUS\tASSIGNEE")
	for _, e := range entries {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Key, e.Title, e.Kind, e.Source, e.Status, e.Assignee)
	}
	return w.Flush()
}
