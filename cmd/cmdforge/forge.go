// Package cmdforge is the CLI surface for code hosts: the backends that open
// pull requests. It is a sibling of cmdtracker, not a corner of it — the two
// domains share no type and no list ([SC-3876]).
package cmdforge

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/forge"
)

// Entry is the JSON output structure for a single configured code host.
type Entry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
}

// BuildForgeCmd creates the "forge" command with its list subcommand. The
// loader is injected so the command can be exercised without a config file.
func BuildForgeCmd(loader func(string) ([]forge.Instance, error)) *cobra.Command {
	forgeCmd := &cobra.Command{
		Use:   "forge",
		Short: "Manage code hosts (pull requests)",
	}

	var table bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List configured code hosts (JSON)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunForgeList(cmd.OutOrStdout(), ".", table, loader)
		},
	}
	listCmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")

	forgeCmd.AddCommand(listCmd)
	return forgeCmd
}

// RunForgeList lists the configured code hosts.
//
// An empty listing is the interesting case, not the boring one: with no forge
// configured nothing can open a pull request, and every deploy will stop at that
// step. So it answers with the same instructions the failure itself gives rather
// than an empty array and no comment.
func RunForgeList(out io.Writer, dir string, table bool, loader func(string) ([]forge.Instance, error)) error {
	if dir == "" {
		dir = "."
	}
	instances, err := loader(dir)
	if err != nil {
		return err
	}

	entries := make([]Entry, len(instances))
	for i, inst := range instances {
		entries[i] = Entry{Name: inst.Name, Kind: inst.Kind, URL: inst.URL}
	}

	if table {
		return PrintTable(out, entries)
	}
	return PrintJSON(out, entries)
}

// PrintJSON prints forge entries as JSON.
func PrintJSON(out io.Writer, entries []Entry) error {
	if len(entries) == 0 {
		if _, err := fmt.Fprintf(out, "// %s\n", forge.NoForgeConfigured("github")); err != nil {
			return err
		}
	}
	if entries == nil {
		entries = []Entry{}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

// PrintTable prints forge entries as a table.
func PrintTable(out io.Writer, entries []Entry) error {
	if len(entries) == 0 {
		_, err := fmt.Fprintln(out, forge.NoForgeConfigured("github"))
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tKIND\tURL")
	for _, e := range entries {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", e.Name, e.Kind, e.URL)
	}
	return w.Flush()
}
