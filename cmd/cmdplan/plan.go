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
	"github.com/gethuman-sh/human/internal/marker"
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
	planCmd.AddCommand(buildPlanDeferCmd(deps))
	return planCmd
}

// buildPlanDeferCmd surfaces the sanctioned-deferral act as one atomic verb:
// when the planner's DECISION REQUIRED fork resolves to "ship the narrow slice
// now + follow-on for the rest", this creates the real follow-on ticket, links
// it related to the PM ticket, and posts the durable [human:shipped-partial]
// trace — the single origin of partial-delivery visibility (SC-2910).
func buildPlanDeferCmd(deps cmdutil.Deps) *cobra.Command {
	var title, description string
	var deferred []string
	cmd := &cobra.Command{
		Use:   "defer PM_KEY",
		Short: "Spawn a linked follow-on ticket for deferred criteria and mark the PM ticket shipped-partial",
		Long: `Record a sanctioned partial delivery: create a follow-on ticket carrying the
deferred acceptance criteria, link it as related to the PM ticket, and post a
[human:shipped-partial] marker on the PM ticket naming each deferred criterion
and the new follow-on key. Invoked by the planner when its DECISION REQUIRED
fork resolves to "ship the narrow slice now + follow-on for the rest".`,
		Example: `  human plan defer SC-2910 --title "Per-ticket cost export" \
    --deferred "CSV export of the cost ledger" --deferred "cost webhook"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer resolved.Cleanup()
			return RunPlanDefer(cmd.Context(), resolved.Provider, cmd.OutOrStdout(), resolved.Key, title, description, deferred)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "Title of the follow-on ticket")
	_ = cmd.MarkFlagRequired("title")
	cmd.Flags().StringVar(&description, "description", "", "Extra markdown appended to the follow-on ticket body (optional)")
	cmd.Flags().StringArrayVar(&deferred, "deferred", nil, "A deferred acceptance criterion (repeatable; at least one required)")
	return cmd
}

// RunPlanDefer creates the follow-on ticket, links it related to the PM ticket,
// and posts the [human:shipped-partial] marker. The three steps run in order so
// the marker never names a ticket that was not created and linked first.
func RunPlanDefer(ctx context.Context, p tracker.Provider, out io.Writer, pmKey, title, description string, deferred []string) error {
	if strings.TrimSpace(title) == "" {
		return errors.WithDetails("follow-on ticket title must not be empty", "pm", pmKey)
	}
	criteria := trimmedNonEmpty(deferred)
	if len(criteria) == 0 {
		return errors.WithDetails("at least one --deferred acceptance criterion is required", "pm", pmKey)
	}

	orig, err := p.GetIssue(ctx, pmKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading PM ticket to file the follow-on in its project", "pm", pmKey)
	}

	body := followOnBody(pmKey, orig.Title, criteria, description)
	created, err := p.CreateIssue(ctx, &tracker.Issue{Project: orig.Project, Title: title, Description: body})
	if err != nil {
		return errors.WrapWithDetails(err, "creating follow-on ticket", "pm", pmKey, "project", orig.Project)
	}

	// The follow-on inherits ownership from whoever deferred the work (SC-3345).
	_, _ = tracker.AssignToCurrentUserBestEffort(ctx, p, created.Key)

	if err := p.LinkIssues(ctx, pmKey, created.Key, tracker.LinkRelated); err != nil {
		return errors.WrapWithDetails(err, "linking follow-on ticket to PM ticket", "pm", pmKey, "follow_on", created.Key)
	}

	m := marker.Marker{
		Type: "shipped-partial",
		Fields: map[string]string{
			"follow-on": created.Key,
			"deferred":  strings.Join(criteria, "\n"),
		},
	}
	if err := marker.Validate(m); err != nil {
		return errors.WrapWithDetails(err, "validating shipped-partial marker", "pm", pmKey)
	}
	rendered := marker.Render(m, []string{"follow-on", "deferred"})
	if _, err := p.AddComment(ctx, pmKey, rendered); err != nil {
		return errors.WrapWithDetails(err, "posting shipped-partial marker", "pm", pmKey)
	}

	_, err = fmt.Fprintf(out, "created follow-on %s (linked related to %s)\n%s\n", created.Key, pmKey, rendered)
	return err
}

// followOnBody composes the follow-on ticket description: a backlink to the PM
// ticket it was split from and a bullet per deferred acceptance criterion, so
// the follow-on stands on its own as the ticket that now owns the deferred work.
func followOnBody(pmKey, pmTitle string, criteria []string, extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Deferred from %s", pmKey)
	if strings.TrimSpace(pmTitle) != "" {
		fmt.Fprintf(&b, " (%s)", pmTitle)
	}
	b.WriteString(".\n\nThis ticket carries the acceptance criteria deliberately deferred when ")
	b.WriteString(pmKey)
	b.WriteString(" shipped its narrow slice:\n\n")
	for _, c := range criteria {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	if strings.TrimSpace(extra) != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(extra))
		b.WriteString("\n")
	}
	return b.String()
}

// trimmedNonEmpty drops blank entries and trims each remaining criterion, so a
// stray empty --deferred flag never becomes an empty bullet or marker line.
func trimmedNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
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
			// ParseBody, not TrimPrefix: a signed plan comment carries machine:/build:
			// between the header and the plan, and trimming only the header prefixed
			// every rendered plan with the signature.
			body = marker.Prose(trimmed)
		}
	}
	return body, haveLatest
}
