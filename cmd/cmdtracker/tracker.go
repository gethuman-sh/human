package cmdtracker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/tracker"
)

// TrackerEntry is the JSON output structure for a single tracker instance.
type TrackerEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	URL         string `json:"url"`
	User        string `json:"user"`
	Role        string `json:"role,omitempty"`
	Description string `json:"description"`
}

// FindResultEntry is the JSON output structure for tracker find.
type FindResultEntry struct {
	Provider string `json:"provider"`
	Project  string `json:"project"`
	Key      string `json:"key"`
}

// BuildTrackerCmd creates the "tracker" command with list and find subcommands.
func BuildTrackerCmd(loader func(string) ([]tracker.Instance, error)) *cobra.Command {
	trackerCmd := &cobra.Command{
		Use:   "tracker",
		Short: "Manage tracker connections",
	}

	var table bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List configured tracker instances (JSON)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunTrackerList(cmd.OutOrStdout(), ".", table, loader)
		},
	}
	listCmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")

	var findTable bool
	findCmd := &cobra.Command{
		Use:   "find KEY",
		Short: "Find which tracker owns a key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunTrackerFind(cmd.Context(), cmd.OutOrStdout(), ".", args[0], findTable, loader)
		},
	}
	findCmd.Flags().BoolVar(&findTable, "table", false, "Output as human-readable table instead of JSON")

	trackerCmd.AddCommand(listCmd, findCmd)
	return trackerCmd
}

// RunTrackerList lists configured tracker instances.
func RunTrackerList(out io.Writer, dir string, table bool, loader func(string) ([]tracker.Instance, error)) error {
	if dir == "" {
		dir = "."
	}
	instances, err := loader(dir)
	if err != nil {
		return err
	}

	entries := make([]TrackerEntry, len(instances))
	for i, inst := range instances {
		entries[i] = TrackerEntry{Name: inst.Name, Type: inst.Kind, URL: inst.URL, User: inst.User, Role: inst.InferRole(), Description: inst.Description}
	}

	if table {
		return PrintTrackerTable(out, entries)
	}
	return PrintTrackerJSON(out, entries)
}

// RunTrackerFind finds which tracker owns a key.
func RunTrackerFind(ctx context.Context, out io.Writer, dir, key string, table bool, loader func(string) ([]tracker.Instance, error)) error {
	if dir == "" {
		dir = "."
	}
	instances, err := loader(dir)
	if err != nil {
		return err
	}
	return RunTrackerFindWithInstances(ctx, out, key, instances, table)
}

// RunTrackerFindWithInstances finds which tracker owns a key given pre-loaded instances.
func RunTrackerFindWithInstances(ctx context.Context, out io.Writer, key string, instances []tracker.Instance, table bool) error {
	result, err := tracker.FindTracker(ctx, key, instances)
	if err != nil {
		return err
	}

	entry := FindResultEntry{
		Provider: result.Provider,
		Project:  result.Project,
		Key:      result.Key,
	}

	if table {
		return PrintFindTable(out, entry)
	}
	return PrintFindJSON(out, entry)
}

// PrintTrackerJSON prints tracker entries as JSON.
func PrintTrackerJSON(w io.Writer, entries []TrackerEntry) error {
	_, _ = fmt.Fprintln(w, "// Configured issue trackers. Use --tracker=<name> to select one.")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

// PrintTrackerTable prints tracker entries as a table.
func PrintTrackerTable(out io.Writer, entries []TrackerEntry) error {
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(out, "No trackers configured in .humanconfig")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tTYPE\tROLE\tURL\tUSER\tDESCRIPTION")
	for _, e := range entries {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, e.Type, e.Role, e.URL, e.User, e.Description)
	}
	return w.Flush()
}

// PrintFindJSON prints a find result as JSON.
func PrintFindJSON(w io.Writer, entry FindResultEntry) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entry)
}

// PrintFindTable prints a find result as a table.
func PrintFindTable(out io.Writer, entry FindResultEntry) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "PROVIDER\tPROJECT\tKEY")
	_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", entry.Provider, entry.Project, entry.Key)
	return w.Flush()
}
