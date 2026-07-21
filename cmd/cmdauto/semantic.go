package cmdauto

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// ideaLabels are the label spellings that mark a ticket as a raw idea. Both
// forms classify (see CLAUDE.md "Tickets"), so promotion removes both.
var ideaLabels = []string{"human/idea", "idea"}

// BuildAutoDoneCmd creates the top-level "done" command: transition an issue
// to its tracker's done-type status without knowing the status name.
func BuildAutoDoneCmd(deps cmdutil.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "done KEY_OR_URL",
		Short: "Move an issue to its done-type status (auto-detect tracker and status name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer result.Cleanup()
			return RunSemanticTransition(cmd.Context(), result.Provider, cmd.OutOrStdout(), result.Key, tracker.CategoryDone, false)
		},
	}
}

// BuildAutoCloseCmd creates the top-level "close" command: transition an issue
// to a closed/cancelled-type status, falling back to done-type on trackers
// whose workflow has no closed bucket.
func BuildAutoCloseCmd(deps cmdutil.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "close KEY_OR_URL",
		Short: "Move an issue to a closed-type status (falls back to done-type when the workflow has none)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer result.Cleanup()
			return RunSemanticTransition(cmd.Context(), result.Provider, cmd.OutOrStdout(), result.Key, tracker.CategoryClosed, true)
		},
	}
}

// BuildAutoIdeaCmd creates the top-level "idea" command with the promote
// subcommand: strip the idea labels when an idea graduates to a PM ticket.
func BuildAutoIdeaCmd(deps cmdutil.Deps) *cobra.Command {
	ideaCmd := &cobra.Command{
		Use:   "idea",
		Short: "Idea-stage ticket operations",
	}
	promoteCmd := &cobra.Command{
		Use:   "promote KEY_OR_URL",
		Short: "Remove the idea labels (human/idea, idea) — the ticket keeps its key and history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer result.Cleanup()
			return RunIdeaPromote(cmd.Context(), result.Provider, cmd.OutOrStdout(), result.Key)
		},
	}
	ideaCmd.AddCommand(promoteCmd)
	return ideaCmd
}

// RunSemanticTransition moves the issue to the first status of the wanted
// category. List order is the tracker's workflow order, so the pick is stable.
// With fallbackToDone, a workflow without a closed bucket closes as done —
// the closest terminal state the tracker offers.
func RunSemanticTransition(ctx context.Context, p tracker.Provider, out io.Writer, key string, want tracker.Category, fallbackToDone bool) error {
	statuses, err := p.ListStatuses(ctx, key)
	if err != nil {
		return err
	}
	status, ok := firstOfCategory(statuses, want)
	if !ok && fallbackToDone {
		if status, ok = firstOfCategory(statuses, tracker.CategoryDone); ok {
			_, _ = fmt.Fprintf(out, "No %s-type status in this workflow — using done-type %q\n", want, status.Name)
		}
	}
	if !ok {
		return errors.WithDetails("workflow has no status of the wanted category",
			"key", key, "category", string(want))
	}
	if err := p.TransitionIssue(ctx, key, status.Name); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Transitioned %s to %s\n", key, status.Name)
	return err
}

// RunIdeaPromote strips the idea labels from the issue; labels it does not
// carry are ignored by the edit layer, so promotion is idempotent.
func RunIdeaPromote(ctx context.Context, p tracker.Provider, out io.Writer, key string) error {
	if _, err := p.EditIssue(ctx, key, tracker.EditOptions{RemoveLabels: ideaLabels}); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "Promoted %s: removed idea labels\n", key)
	return err
}

func firstOfCategory(statuses []tracker.Status, want tracker.Category) (tracker.Status, bool) {
	for _, s := range statuses {
		if s.Category == want {
			return s, true
		}
	}
	return tracker.Status{}, false
}
