// Package cmdreview surfaces the durable review-findings record as a question
// an agent can ask before it edits a file: "what did past reviews find here?".
// The record has been written since SC-5278 and had no reader (SC-5398).
package cmdreview

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/env"
	"github.com/gethuman-sh/human/internal/recall"
)

// Deps are the injectable collaborators of the review commands.
type Deps struct {
	DBPath func() string
	// NewStore opens the record. The caller takes ownership of what it
	// returns and closes it, so it must hand back a fresh store per call.
	NewStore func(dbPath string) (recall.FindingsReader, error)
	Project  func(ctx context.Context) string
}

// DefaultDeps returns production dependencies.
func DefaultDeps() Deps {
	return Deps{
		DBPath: recall.DefaultDBPath,
		NewStore: func(dbPath string) (recall.FindingsReader, error) {
			return recall.NewSQLiteStore(dbPath)
		},
		Project: func(ctx context.Context) string {
			// The daemon injects the project per request, resolved from --key;
			// empty means the default project (single-project and direct CLI).
			// Never os.Getenv: the value arrives on the forwarded request, not
			// in the daemon's own process environment.
			return agentstate.NormalizeProject(env.Lookup(ctx, "HUMAN_STATE_PROJECT"))
		},
	}
}

// BuildReviewCmd creates the "review" command group.
func BuildReviewCmd(deps Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Machine review record",
		Long:  "Read what the machine review loop has recorded about this codebase.",
	}
	cmd.AddCommand(buildFindingsCmd(deps))
	return cmd
}

func buildFindingsCmd(deps Deps) *cobra.Command {
	var (
		key     string
		limit   int
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "findings PATH [PATH...]",
		Short: "Prior machine-review findings for source paths",
		Long: "List the blocking findings past machine review rounds recorded against these files, newest first,\n" +
			"with the reviewer's class, the ticket and round they came from, and what the fixer did about each.\n\n" +
			"It advises: no gate reads it and nothing is blocked by what it returns. \"No prior findings recorded\n" +
			"for these files.\" is a real answer, not a failure.\n\n" +
			"The record covers the pull-request review loop only. A file reviewed by the pre-merge reviewer can\n" +
			"have no rows here; that is a gap in the record, not evidence the file was never criticised.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunFindings(cmd.Context(), cmd.OutOrStdout(), args, key, limit, jsonOut, deps)
		},
	}

	cmd.Flags().StringVar(&key, "key", "", "Ticket key whose project the record is read from")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of findings to return")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// RunFindings answers "what did past reviews find in these files".
//
// key is declared and accepted but not read here: the daemon resolves the
// project from it while forwarding the request and injects the answer as
// HUMAN_STATE_PROJECT. The flag must still exist or cobra rejects the
// forwarded invocation.
func RunFindings(ctx context.Context, out io.Writer, paths []string, key string, limit int, jsonOut bool, deps Deps) error {
	_ = key
	normalized := make([]string, 0, len(paths))
	for _, p := range paths {
		if n := daemon.NormalizeFindingFile(p); n != "" {
			normalized = append(normalized, n)
		}
	}

	store, err := deps.NewStore(deps.DBPath())
	if err != nil {
		return err
	}
	if closer, ok := store.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}

	// A record that cannot be read is an error; a record with nothing to say
	// is an empty answer. Conflating the two would make a broken recorder look
	// like a clean file.
	found, err := store.FindingsForFiles(ctx, deps.Project(ctx), normalized, limit)
	if err != nil {
		return err
	}

	if jsonOut {
		if found == nil {
			// PrintJSON encodes a nil slice as `null`, and a caller parsing
			// `null` where it expects a list reads an empty record as a
			// malformed answer — the one thing this command must never do.
			found = []recall.ReviewFinding{}
		}
		return cmdutil.PrintJSON(out, found)
	}

	if len(found) == 0 {
		_, _ = fmt.Fprintln(out, "No prior findings recorded for these files.")
		return nil
	}

	printFindings(out, found)
	return nil
}

// printFindings groups by the row's file so a reader scanning several paths
// sees each file's history together, in the order the query returned them.
func printFindings(out io.Writer, found []recall.ReviewFinding) {
	current := ""
	for _, f := range found {
		if f.File != current {
			if current != "" {
				_, _ = fmt.Fprintln(out)
			}
			current = f.File
			_, _ = fmt.Fprintln(out, f.File)
		}
		_, _ = fmt.Fprintf(out, "  [%s] %s PR %d round %d — %s\n", classOf(f), f.Key, f.PR, f.Round, f.Slug)
		if f.Disposition != "" {
			_, _ = fmt.Fprintf(out, "    %s\n", f.Disposition)
		}
		if f.Note != "" {
			_, _ = fmt.Fprintf(out, "    note: %s\n", f.Note)
		}
		if text := strings.TrimSpace(f.Text); text != "" {
			_, _ = fmt.Fprintf(out, "    %s\n", text)
		}
	}
}

// classOf names the reviewer's category, or says plainly that there was none —
// a blank column would read as a formatting glitch rather than as a fact.
func classOf(f recall.ReviewFinding) string {
	if f.Class == "" {
		return "unclassified"
	}
	return f.Class
}
