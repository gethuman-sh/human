package cmdnotion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/knowledge/notion"
)

// notionSearcher searches the Notion workspace.
type notionSearcher interface {
	Search(ctx context.Context, query string) ([]notion.SearchResult, error)
}

// notionPageGetter retrieves a page as markdown.
type notionPageGetter interface {
	GetPage(ctx context.Context, pageID string) (string, error)
}

// notionDatabaseQuerier queries a database.
type notionDatabaseQuerier interface {
	QueryDatabase(ctx context.Context, dbID string) ([]notion.DatabaseRow, error)
}

// notionDatabaseLister lists shared databases.
type notionDatabaseLister interface {
	ListDatabases(ctx context.Context) ([]notion.DatabaseEntry, error)
}

func BuildNotionCommands() *cobra.Command {
	notionCmd := &cobra.Command{
		Use:   "notion",
		Short: "Notion workspace tools",
	}

	notionCmd.PersistentFlags().String("notion", "", "Named Notion instance from .humanconfig")

	// --- search ---
	var searchTable bool
	searchCmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search Notion workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveNotionClient(cmd)
			if err != nil {
				return err
			}
			return runNotionSearch(cmd.Context(), client, cmd.OutOrStdout(), args[0], searchTable)
		},
	}
	searchCmd.Flags().BoolVar(&searchTable, "table", false, "Output as human-readable table instead of JSON")
	notionCmd.AddCommand(searchCmd)

	// --- page ---
	pageCmd := &cobra.Command{
		Use:   "page",
		Short: "Page operations",
	}
	pageGetCmd := &cobra.Command{
		Use:   "get PAGE_ID",
		Short: "Get page content as markdown",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveNotionClient(cmd)
			if err != nil {
				return err
			}
			return runNotionPageGet(cmd.Context(), client, cmd.OutOrStdout(), args[0])
		},
	}
	pageCmd.AddCommand(pageGetCmd)
	notionCmd.AddCommand(pageCmd)

	// --- database ---
	databaseCmd := &cobra.Command{
		Use:   "database",
		Short: "Database operations",
	}
	var queryTable bool
	queryCmd := &cobra.Command{
		Use:   "query DATABASE_ID",
		Short: "Query database rows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveNotionClient(cmd)
			if err != nil {
				return err
			}
			return runNotionDatabaseQuery(cmd.Context(), client, cmd.OutOrStdout(), args[0], queryTable)
		},
	}
	queryCmd.Flags().BoolVar(&queryTable, "table", false, "Output as human-readable table instead of JSON")
	databaseCmd.AddCommand(queryCmd)
	notionCmd.AddCommand(databaseCmd)

	// --- databases ---
	databasesCmd := &cobra.Command{
		Use:   "databases",
		Short: "List databases",
	}
	var listTable bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List shared databases",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := resolveNotionClient(cmd)
			if err != nil {
				return err
			}
			return runNotionDatabasesList(cmd.Context(), client, cmd.OutOrStdout(), listTable)
		},
	}
	listCmd.Flags().BoolVar(&listTable, "table", false, "Output as human-readable table instead of JSON")
	databasesCmd.AddCommand(listCmd)
	notionCmd.AddCommand(databasesCmd)

	return notionCmd
}

func resolveNotionClient(cmd *cobra.Command) (*notion.Client, error) {
	name, _ := cmd.Flags().GetString("notion")

	instances, err := notion.LoadInstances(".")
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, errors.WithDetails("no Notion instances configured, add notions: to .humanconfig.yaml")
	}

	if name != "" {
		for _, inst := range instances {
			if inst.Name == name {
				return inst.Client, nil
			}
		}
		return nil, errors.WithDetails("Notion instance not found", "name", name)
	}

	return instances[0].Client, nil
}

// --- Business logic functions ---

func runNotionSearch(ctx context.Context, client notionSearcher, out io.Writer, query string, table bool) error {
	results, err := client.Search(ctx, query)
	if err != nil {
		return err
	}
	if table {
		return printSearchTable(out, results)
	}
	return printSearchJSON(out, results)
}

func runNotionPageGet(ctx context.Context, client notionPageGetter, out io.Writer, pageID string) error {
	md, err := client.GetPage(ctx, pageID)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprint(out, md)
	return nil
}

func runNotionDatabaseQuery(ctx context.Context, client notionDatabaseQuerier, out io.Writer, dbID string, table bool) error {
	rows, err := client.QueryDatabase(ctx, dbID)
	if err != nil {
		return err
	}
	if table {
		return printDatabaseRowsTable(out, rows)
	}
	return printDatabaseRowsJSON(out, rows)
}

func runNotionDatabasesList(ctx context.Context, client notionDatabaseLister, out io.Writer, table bool) error {
	entries, err := client.ListDatabases(ctx)
	if err != nil {
		return err
	}
	if table {
		return printDatabasesTable(out, entries)
	}
	return printDatabasesJSON(out, entries)
}

// --- Output formatters ---

func printSearchJSON(w io.Writer, results []notion.SearchResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func printSearchTable(out io.Writer, results []notion.SearchResult) error {
	if len(results) == 0 {
		_, _ = fmt.Fprintln(out, "No results found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tTYPE\tTITLE\tURL")
	for _, r := range results {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, r.Type, r.Title, r.URL)
	}
	return w.Flush()
}

func printDatabasesJSON(w io.Writer, entries []notion.DatabaseEntry) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

func printDatabasesTable(out io.Writer, entries []notion.DatabaseEntry) error {
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(out, "No databases found")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tTITLE\tURL")
	for _, e := range entries {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", e.ID, e.Title, e.URL)
	}
	return w.Flush()
}

func printDatabaseRowsJSON(w io.Writer, rows []notion.DatabaseRow) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

func printDatabaseRowsTable(out io.Writer, rows []notion.DatabaseRow) error {
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(out, "No rows found")
		return nil
	}

	// Collect all property keys from all rows.
	keySet := make(map[string]bool)
	for _, row := range rows {
		for k := range row.Properties {
			keySet[k] = true
		}
	}
	var keys []string
	for k := range keySet {
		keys = append(keys, k)
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

	// Header.
	header := "ID"
	for _, k := range keys {
		header += "\t" + k
	}
	_, _ = fmt.Fprintln(w, header)

	// Rows.
	for _, row := range rows {
		line := row.ID
		for _, k := range keys {
			line += "\t" + row.Properties[k]
		}
		_, _ = fmt.Fprintln(w, line)
	}
	return w.Flush()
}
