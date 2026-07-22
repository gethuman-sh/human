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
	"github.com/gethuman-sh/human/internal/tracker"
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

	keysCmd := &cobra.Command{
		Use:   "keys [PATH...]",
		Short: "List ticket keys referenced by commits touching the paths (prefixed keys first, deduped)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunCommitKeys(cmd.Context(), cmd.OutOrStdout(), ".", args)
		},
	}

	var recencyRef string
	touchedCmd := &cobra.Command{
		Use:   "touched [PATH...]",
		Short: "Report whether the paths changed since the recency boundary (latest tag, else 30 days)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunCommitsTouched(cmd.Context(), cmd.OutOrStdout(), ".", recencyRef, args)
		},
	}
	touchedCmd.Flags().StringVar(&recencyRef, "ref", "", "Boundary ref (default: resolved recency boundary)")

	recencyCmd := &cobra.Command{
		Use:   "recency",
		Short: "Print the resolved recency boundary: latest tag, or the 30-day fallback window",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunCommitsRecency(cmd.Context(), cmd.OutOrStdout(), ".")
		},
	}

	commitsCmd.AddCommand(forCmd, prefixCmd, keysCmd, touchedCmd, recencyCmd)
	return commitsCmd
}

// RunCommitKeys prints the ticket keys referenced by commits touching paths.
func RunCommitKeys(ctx context.Context, out io.Writer, dir string, paths []string) error {
	keys, err := gitrepo.TicketKeys(ctx, dir, paths)
	if err != nil {
		return err
	}
	if keys == nil {
		keys = []string{}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(keys)
}

// RunCommitsRecency prints the resolved recency boundary as JSON: {"tag": ...}
// when a tag exists, {"since": "30 days ago"} otherwise.
func RunCommitsRecency(ctx context.Context, out io.Writer, dir string) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if tag := gitrepo.LatestTag(ctx, dir); tag != "" {
		return enc.Encode(map[string]string{"tag": tag})
	}
	return enc.Encode(map[string]string{"since": "30 days ago"})
}

// RunCommitsTouched prints true/false: did any commit after the boundary touch
// the paths. An explicit --ref wins; otherwise the resolved recency boundary
// applies.
func RunCommitsTouched(ctx context.Context, out io.Writer, dir, ref string, paths []string) error {
	boundary := ref
	if boundary == "" {
		boundary = gitrepo.LatestTag(ctx, dir)
	}
	touched, err := gitrepo.TouchedSince(ctx, dir, boundary, paths)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, touched)
	return err
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
		// Canonicalize before bracketing so a bare numeric key (the form the
		// board passes internally) resolves to the tracker-attributable
		// reference rather than an orphaned "[1117]" (SC-1134).
		parts[i] = "[" + tracker.CanonicalCommitKey(key) + "]"
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
