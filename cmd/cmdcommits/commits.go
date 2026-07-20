// Package cmdcommits surfaces the deterministic commit mechanics agents
// otherwise hand-roll in prompts: discovering which commits reference a ticket
// key, and assembling the bracket-style commit-message prefix that carries the
// PM → engineering → commit trail.
package cmdcommits

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/gitrepo"
)

// BuildCommitsCmd creates the "commits" command with for and prefix subcommands.
func BuildCommitsCmd() *cobra.Command {
	commitsCmd := &cobra.Command{
		Use:   "commits",
		Short: "Deterministic commit mechanics for ticket keys",
	}

	var forTable bool
	forCmd := &cobra.Command{
		Use:   "for KEY",
		Short: "List commits referencing a ticket key (any accepted reference format, merge PRs excluded)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunCommitsFor(cmd.Context(), cmd.OutOrStdout(), ".", args[0], forTable)
		},
	}
	forCmd.Flags().BoolVar(&forTable, "table", false, "Output as human-readable table instead of JSON")

	prefixCmd := &cobra.Command{
		Use:   "prefix KEY [ENGINEERING_KEY]",
		Short: "Print the commit-message prefix for a ticket (PM key first, engineering key second in split topology)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunCommitPrefix(cmd.OutOrStdout(), args)
		},
	}

	commitsCmd.AddCommand(forCmd, prefixCmd)
	return commitsCmd
}

// RunCommitsFor lists commits referencing key in the repository at dir.
func RunCommitsFor(ctx context.Context, out io.Writer, dir, key string, table bool) error {
	commits, err := gitrepo.CommitsFor(ctx, dir, key)
	if err != nil {
		return err
	}
	if table {
		return printCommitsTable(out, commits)
	}
	if commits == nil {
		commits = []gitrepo.Commit{}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(commits)
}

// RunCommitPrefix prints the canonical commit-message prefix for the given
// keys: each key bracketed, PM before engineering, ready to prepend to a
// subject line. Keys already carrying brackets pass through unchanged.
func RunCommitPrefix(out io.Writer, keys []string) error {
	parts := make([]string, len(keys))
	for i, key := range keys {
		trimmed := strings.TrimSpace(key)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			parts[i] = trimmed
			continue
		}
		parts[i] = "[" + trimmed + "]"
	}
	_, err := fmt.Fprintln(out, strings.Join(parts, " "))
	return err
}

func printCommitsTable(out io.Writer, commits []gitrepo.Commit) error {
	if len(commits) == 0 {
		_, _ = fmt.Fprintln(out, "No commits reference this key")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "SHORT\tSUBJECT")
	for _, c := range commits {
		_, _ = fmt.Fprintf(w, "%s\t%s\n", c.ShortSHA, c.Subject)
	}
	return w.Flush()
}
