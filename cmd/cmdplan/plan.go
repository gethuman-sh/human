// Package cmdplan surfaces the [human:plan] comment — the engineering plan a
// ticket carries in single-tracker topology — as a first-class CLI object, so
// agents and users read "the plan" without knowing it is stored as a comment.
package cmdplan

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// PlanCommentHeader mirrors the daemon's marker constant. Declared here
// rather than imported so the CLI package does not pull in the daemon.
const PlanCommentHeader = "[human:plan]"

// BuildPlanCmd creates the top-level "plan" command.
func BuildPlanCmd(deps cmdutil.Deps) *cobra.Command {
	planCmd := &cobra.Command{
		Use:   "plan",
		Short: "Engineering plan attached to a ticket",
	}
	planCmd.AddCommand(buildPlanShowCmd(deps))
	return planCmd
}

func buildPlanShowCmd(deps cmdutil.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "show KEY",
		Short: "Print the ticket's [human:plan] comment (auto-detect tracker)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer result.Cleanup()
			return runPlanShow(cmd.Context(), result.Provider, cmd.OutOrStdout(), result.Key)
		},
	}
}

// runPlanShow prints the latest plan comment's body, header stripped. The
// latest wins so a re-plan supersedes older plans without history edits.
func runPlanShow(ctx context.Context, p tracker.Provider, out io.Writer, key string) error {
	comments, err := p.ListComments(ctx, key)
	if err != nil {
		return err
	}
	body, ok := ExtractPlan(comments)
	if !ok {
		return errors.WithDetails("no [human:plan] comment on ticket", "key", key)
	}
	_, err = fmt.Fprintln(out, body)
	return err
}

// ExtractPlan returns the newest [human:plan] comment body with the header
// line stripped.
func ExtractPlan(comments []tracker.Comment) (string, bool) {
	var body string
	var haveLatest bool
	latestIdx := -1
	for i, c := range comments {
		trimmed := strings.TrimSpace(c.Body)
		if !strings.HasPrefix(trimmed, PlanCommentHeader) {
			continue
		}
		if !haveLatest || c.Created.After(comments[latestIdx].Created) {
			latestIdx = i
			haveLatest = true
			body = strings.TrimSpace(strings.TrimPrefix(trimmed, PlanCommentHeader))
		}
	}
	return body, haveLatest
}
