package cmdprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
)

// BuildProviderCommands returns the "issues" and "issue" cobra commands
// that use the given provider kind for resolution.
func BuildProviderCommands(kind string, deps cmdutil.Deps) []*cobra.Command {
	issuesCmd := &cobra.Command{
		Use:   "issues",
		Short: "Bulk issue operations",
	}
	issuesCmd.AddCommand(buildIssuesListCmd(kind, deps))

	issueCmd := &cobra.Command{
		Use:   "issue",
		Short: "Single issue operations",
	}
	issueCmd.AddCommand(buildIssueGetCmd(kind, deps))
	issueCmd.AddCommand(buildIssueCreateCmd(kind, deps))
	issueCmd.AddCommand(buildIssueEditCmd(kind, deps))
	issueCmd.AddCommand(buildIssueDeleteCmd(kind, deps))
	issueCmd.AddCommand(buildIssueCommentCmd(kind, deps))
	issueCmd.AddCommand(buildIssueStartCmd(kind, deps))
	issueCmd.AddCommand(buildIssueStatusesCmd(kind, deps))
	issueCmd.AddCommand(buildIssueStatusSetCmd(kind, deps))

	cmds := []*cobra.Command{issuesCmd, issueCmd}

	// Only code forges (GitHub, …) expose pull-request operations; pure issue
	// trackers must not advertise a `pr` command they can't fulfil.
	if forge.IsForgeKind(kind) {
		prCmd := &cobra.Command{
			Use:   "pr",
			Short: "Pull request operations",
		}
		prCmd.AddCommand(buildPRCreateCmd(kind, deps))
		cmds = append(cmds, prCmd)
	}

	return cmds
}

func buildIssuesListCmd(kind string, deps cmdutil.Deps) *cobra.Command {
	var project string
	var all, table bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List project issues (JSON)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, cleanup, err := cmdutil.ResolveProvider(cmd, kind, deps)
			if err != nil {
				return err
			}
			defer cleanup()
			return RunListIssues(cmd.Context(), p, cmd.OutOrStdout(), project, all, table)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project key (Jira: KAN, GitHub: owner/repo, GitLab: group/project, Linear: ENG). Omit to list across all projects.")
	cmd.Flags().BoolVar(&all, "all", false, "Include all issues (default: open only)")
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildIssueGetCmd(kind string, deps cmdutil.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "get KEY",
		Short: "Get a single issue with metadata and description as markdown",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, cleanup, err := cmdutil.ResolveProvider(cmd, kind, deps)
			if err != nil {
				return err
			}
			defer cleanup()
			return RunGetIssue(cmd.Context(), p, cmd.OutOrStdout(), args[0])
		},
	}
}

func buildIssueCreateCmd(kind string, deps cmdutil.Deps) *cobra.Command {
	var project, typ, description, parent string

	cmd := &cobra.Command{
		Use:     "create TITLE",
		Short:   "Create a new issue in a project",
		Example: `  human jira issue create --project=KAN "Implement login page" --description "Add OAuth2 login flow with Google provider"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, cleanup, err := cmdutil.ResolveProvider(cmd, kind, deps)
			if err != nil {
				return err
			}
			defer cleanup()
			return RunCreateIssue(cmd.Context(), p, cmd.OutOrStdout(), project, typ, args[0], description, parent)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project key (Jira: KAN, GitHub: owner/repo, GitLab: group/project, Linear: ENG)")
	_ = cmd.MarkFlagRequired("project")
	cmd.Flags().StringVar(&typ, "type", "Task", "Issue type (Jira only, e.g. Task, Bug, Story)")
	cmd.Flags().StringVar(&description, "description", "", "Issue description in markdown (separate from title)")
	cmd.Flags().StringVar(&parent, "parent", "", "Parent issue key to create this as a subtask (Linear, Jira, Shortcut, Azure DevOps, GitHub, ClickUp; not supported on GitLab)")
	return cmd
}

func buildPRCreateCmd(kind string, deps cmdutil.Deps) *cobra.Command {
	var repo, base, head, title, body string

	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Open a pull request",
		Example: `  human github pr create --repo=octocat/hello-world --head=fix-login --base=main --title "Fix login" --body "Closes #42"`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := cmdutil.ResolveForge(cmd, kind, deps)
			if err != nil {
				return err
			}
			if repo == "" {
				// Default to the repository of the local git origin remote.
				repo, err = cmdutil.OriginRepo(cmd)
				if err != nil {
					return err
				}
			}
			return RunCreatePullRequest(cmd.Context(), f, cmd.OutOrStdout(), repo, base, head, title, body)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "Repository (GitHub: owner/repo); defaults to the git origin remote")
	cmd.Flags().StringVar(&head, "head", "", "Head branch holding the changes")
	_ = cmd.MarkFlagRequired("head")
	cmd.Flags().StringVar(&base, "base", "main", "Base branch to merge into")
	cmd.Flags().StringVar(&title, "title", "", "Pull request title")
	_ = cmd.MarkFlagRequired("title")
	cmd.Flags().StringVar(&body, "body", "", "Pull request description in markdown")
	return cmd
}

func buildIssueEditCmd(kind string, deps cmdutil.Deps) *cobra.Command {
	var title, description string
	var yes bool

	cmd := &cobra.Command{
		Use:     "edit KEY",
		Short:   "Edit an issue's title and/or description",
		Example: `  human jira issue edit KAN-1 --title "New title" --description "Updated description"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("title") && !cmd.Flags().Changed("description") {
				return errors.WithDetails("at least one of --title or --description is required")
			}
			p, cleanup, err := cmdutil.ResolveProvider(cmd, kind, deps)
			if err != nil {
				return err
			}
			defer cleanup()

			var opts tracker.EditOptions
			if cmd.Flags().Changed("title") {
				opts.Title = &title
			}
			if cmd.Flags().Changed("description") {
				opts.Description = &description
			}

			_ = yes // consumed by daemon interceptor via --yes flag; unused here
			return RunEditIssue(cmd.Context(), p, cmd.OutOrStdout(), args[0], opts)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "New issue title")
	cmd.Flags().StringVar(&description, "description", "", "New issue description (markdown)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip interactive confirmation")
	return cmd
}

func buildIssueDeleteCmd(kind string, deps cmdutil.Deps) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "delete KEY",
		Short: "Delete (or close) an issue by key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, cleanup, err := cmdutil.ResolveProvider(cmd, kind, deps)
			if err != nil {
				return err
			}
			defer cleanup()
			return RunDeleteIssue(cmd.Context(), p, os.Stdin, cmd.OutOrStdout(), args[0], yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip interactive confirmation")
	return cmd
}

func buildIssueCommentCmd(kind string, deps cmdutil.Deps) *cobra.Command {
	commentCmd := &cobra.Command{
		Use:   "comment",
		Short: "Comment operations on an issue",
	}

	addCmd := &cobra.Command{
		Use:   "add KEY BODY",
		Short: "Add a comment to an issue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, cleanup, err := cmdutil.ResolveProvider(cmd, kind, deps)
			if err != nil {
				return err
			}
			defer cleanup()
			return RunAddComment(cmd.Context(), p, cmd.OutOrStdout(), args[0], args[1])
		},
	}

	listCmd := &cobra.Command{
		Use:   "list KEY",
		Short: "List comments on an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, cleanup, err := cmdutil.ResolveProvider(cmd, kind, deps)
			if err != nil {
				return err
			}
			defer cleanup()
			return RunListComments(cmd.Context(), p, cmd.OutOrStdout(), args[0])
		},
	}

	commentCmd.AddCommand(addCmd, listCmd)
	return commentCmd
}

func buildIssueStartCmd(kind string, deps cmdutil.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "start KEY",
		Short: "Start working on an issue (transition to In Progress and assign to yourself)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, cleanup, err := cmdutil.ResolveProvider(cmd, kind, deps)
			if err != nil {
				return err
			}
			defer cleanup()
			return RunStartIssue(cmd.Context(), p, cmd.OutOrStdout(), args[0])
		},
	}
}

func buildIssueStatusesCmd(kind string, deps cmdutil.Deps) *cobra.Command {
	var table bool

	cmd := &cobra.Command{
		Use:     "statuses KEY",
		Short:   "List available statuses for an issue",
		Example: `  human jira issue statuses KAN-1`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, cleanup, err := cmdutil.ResolveProvider(cmd, kind, deps)
			if err != nil {
				return err
			}
			defer cleanup()
			return RunListStatuses(cmd.Context(), p, cmd.OutOrStdout(), args[0], table)
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

func buildIssueStatusSetCmd(kind string, deps cmdutil.Deps) *cobra.Command {
	return &cobra.Command{
		Use:     "status KEY STATUS",
		Short:   "Set the status of an issue",
		Example: `  human jira issue status KAN-1 "In Progress"`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, cleanup, err := cmdutil.ResolveProvider(cmd, kind, deps)
			if err != nil {
				return err
			}
			defer cleanup()
			return RunSetStatus(cmd.Context(), p, cmd.OutOrStdout(), args[0], args[1])
		},
	}
}

// --- Business logic functions (exported for use by cmdauto) ---

// RunListIssues lists issues for a project.
func RunListIssues(ctx context.Context, p tracker.Provider, out io.Writer, project string, all, table bool) error {
	issues, err := p.ListIssues(ctx, tracker.ListOptions{
		Project:    project,
		MaxResults: 50,
		IncludeAll: all,
	})
	if err != nil {
		return err
	}
	if table {
		return printIssuesTable(out, issues)
	}
	return printIssuesJSON(out, issues)
}

// RunGetIssue retrieves and prints a single issue.
func RunGetIssue(ctx context.Context, p tracker.Provider, out io.Writer, key string) error {
	issue, err := p.GetIssue(ctx, key)
	if err != nil {
		return err
	}
	if issue == nil {
		return errors.WithDetails("get returned no issue", "key", key)
	}

	displayOrNone := func(s string) string {
		if s == "" {
			return "None"
		}
		return s
	}

	_, _ = fmt.Fprintf(out, "# %s: %s\n\n", issue.Key, issue.Title)
	_, _ = fmt.Fprintln(out, "| Field    | Value       |")
	_, _ = fmt.Fprintln(out, "|----------|-------------|")
	_, _ = fmt.Fprintf(out, "| Status   | %s |\n", issue.Status)
	_, _ = fmt.Fprintf(out, "| Priority | %s |\n", displayOrNone(issue.Priority))
	_, _ = fmt.Fprintf(out, "| Assignee | %s |\n", displayOrNone(issue.Assignee))
	_, _ = fmt.Fprintf(out, "| Reporter | %s |\n", displayOrNone(issue.Reporter))
	if issue.ParentKey != "" {
		_, _ = fmt.Fprintf(out, "| Parent   | %s |\n", issue.ParentKey)
	}

	if issue.Description != "" {
		_, _ = fmt.Fprintf(out, "\n## Description\n\n%s", issue.Description)
	}

	return nil
}

// RunCreateIssue creates a new issue. When parent is non-empty, the issue is
// created as a subtask of the given parent key. Subtask support is
// provider-specific (see the --parent flag help); GitLab rejects it.
func RunCreateIssue(ctx context.Context, p tracker.Provider, out io.Writer, project, typ, title, description, parent string) error {
	issue, err := p.CreateIssue(ctx, &tracker.Issue{
		Project:     project,
		Type:        typ,
		Title:       title,
		Description: description,
		ParentKey:   parent,
	})
	if err != nil {
		return err
	}
	if issue == nil {
		return errors.WithDetails("create returned no issue", "project", project)
	}
	_, _ = fmt.Fprintf(out, "%s\t%s\n", issue.Key, issue.Title)
	return nil
}

// RunCreatePullRequest opens a pull request on a code forge and prints the URL.
func RunCreatePullRequest(ctx context.Context, f forge.Creator, out io.Writer, repo, base, head, title, body string) error {
	pr, err := f.CreatePullRequest(ctx, &forge.PullRequest{
		Repo:  repo,
		Base:  base,
		Head:  head,
		Title: title,
		Body:  body,
	})
	if err != nil {
		return err
	}
	if pr == nil {
		return errors.WithDetails("create returned no pull request", "repo", repo)
	}
	_, _ = fmt.Fprintf(out, "%s\t%s\n", pr.URL, pr.Title)
	return nil
}

// RunDeleteIssue deletes an issue.
// When yes is false, the user is prompted for confirmation on in. Empty input
// (bare Enter) is treated as "No" to match the [y/N] default.
func RunDeleteIssue(ctx context.Context, p tracker.Provider, in io.Reader, out io.Writer, key string, yes bool) error {
	if !yes {
		fmt.Fprintf(os.Stderr, "Delete %s? [y/N] ", key)
		scanner := bufio.NewScanner(in)
		if !scanner.Scan() {
			return errors.WithDetails("delete cancelled by user")
		}
		answer := strings.TrimSpace(scanner.Text())
		if answer == "" || (answer[0] != 'y' && answer[0] != 'Y') {
			return errors.WithDetails("delete cancelled by user")
		}
	}
	if err := p.DeleteIssue(ctx, key); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Deleted %s\n", key)
	return nil
}

// RunEditIssue edits an issue's title and/or description.
func RunEditIssue(ctx context.Context, p tracker.Provider, out io.Writer, key string, opts tracker.EditOptions) error {
	issue, err := p.EditIssue(ctx, key, opts)
	if err != nil {
		return err
	}
	if issue == nil {
		return errors.WithDetails("edit returned no issue", "key", key)
	}
	_, _ = fmt.Fprintf(out, "%s\t%s\n", issue.Key, issue.Title)
	return nil
}

// RunStartIssue transitions an issue and assigns to the current user.
func RunStartIssue(ctx context.Context, p tracker.Provider, out io.Writer, key string) error {
	userID, err := p.GetCurrentUser(ctx)
	if err != nil {
		return errors.WrapWithDetails(err, "getting current user")
	}

	transitionErr := p.TransitionIssue(ctx, key, "In Progress")
	assignErr := p.AssignIssue(ctx, key, userID)

	if transitionErr != nil && assignErr != nil {
		return errors.WithDetails("failed to start issue",
			"key", key,
			"transitionError", transitionErr.Error(),
			"assignError", assignErr.Error())
	}

	if transitionErr != nil {
		_, _ = fmt.Fprintf(out, "Assigned %s to %s (transition failed: %v)\n", key, userID, transitionErr)
		return nil
	}

	if assignErr != nil {
		_, _ = fmt.Fprintf(out, "Transitioned %s to In Progress (assign failed: %v)\n", key, assignErr)
		return nil
	}

	_, _ = fmt.Fprintf(out, "Started %s\n", key)
	return nil
}

// RunAddComment adds a comment to an issue.
func RunAddComment(ctx context.Context, p tracker.Provider, out io.Writer, key, body string) error {
	comment, err := p.AddComment(ctx, key, body)
	if err != nil {
		return err
	}
	if comment == nil {
		return errors.WithDetails("add comment returned no comment", "key", key)
	}
	_, _ = fmt.Fprintf(out, "%s\t%s\n", comment.ID, comment.Body)
	return nil
}

// RunListComments lists comments on an issue.
func RunListComments(ctx context.Context, p tracker.Provider, out io.Writer, key string) error {
	comments, err := p.ListComments(ctx, key)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(comments)
}

// RunListStatuses lists available statuses for an issue.
func RunListStatuses(ctx context.Context, p tracker.Provider, out io.Writer, key string, table bool) error {
	statuses, err := p.ListStatuses(ctx, key)
	if err != nil {
		return err
	}
	if table {
		return PrintStatusesTable(out, statuses)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(statuses)
}

// RunSetStatus sets an issue's status.
func RunSetStatus(ctx context.Context, p tracker.Provider, out io.Writer, key, status string) error {
	if err := p.TransitionIssue(ctx, key, status); err != nil {
		_, _ = fmt.Fprintf(out, "Hint: run 'human <tracker> issue statuses %s' to see available statuses\n", key)
		return err
	}
	_, _ = fmt.Fprintf(out, "Transitioned %s to %s\n", key, status)
	return nil
}

// --- Print helpers ---

func printIssuesJSON(w io.Writer, issues []tracker.Issue) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(issues)
}

func printIssuesTable(out io.Writer, issues []tracker.Issue) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "KEY\tSTATUS\tTITLE")
	for _, issue := range issues {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", issue.Key, issue.Status, issue.Title)
	}
	return w.Flush()
}

// PrintStatusesTable prints statuses as a table.
func PrintStatusesTable(out io.Writer, statuses []tracker.Status) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	// Header stays "TYPE" (not "CATEGORY") to keep the public CLI output
	// stable for scripts; internally this column holds a tracker.Category.
	_, _ = fmt.Fprintln(w, "NAME\tTYPE")
	for _, s := range statuses {
		category := string(s.Category)
		if category == "" {
			category = "-"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\n", s.Name, category)
	}
	return w.Flush()
}
