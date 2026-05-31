package cmdauto

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdprovider"
	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/tracker"
)

// BuildAutoGetCmd creates the top-level "get" command that auto-detects the tracker.
func BuildAutoGetCmd(deps cmdutil.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "get KEY_OR_URL",
		Short: "Get an issue (auto-detect tracker)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer result.Cleanup()

			if err := cmdprovider.RunGetIssue(cmd.Context(), result.Provider, cmd.OutOrStdout(), result.Key); err != nil {
				return err
			}

			project := tracker.ExtractProject(result.Key)
			PrintAutoHints(cmd.ErrOrStderr(), result.Kind, result.Key, project, "get")
			return nil
		},
	}
}

// BuildAutoListCmd creates the top-level "list" command that auto-detects the tracker.
func BuildAutoListCmd(deps cmdutil.Deps) *cobra.Command {
	var project string
	var all, table bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List issues (auto-detect tracker)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, project, false, deps)
			if err != nil {
				return err
			}
			defer result.Cleanup()

			if err := cmdprovider.RunListIssues(cmd.Context(), result.Provider, cmd.OutOrStdout(), project, all, table); err != nil {
				return err
			}

			PrintAutoHints(cmd.ErrOrStderr(), result.Kind, "", project, "list")
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project key (Jira: KAN, GitHub: owner/repo, GitLab: group/project, Linear: ENG). Omit to list across all projects.")
	cmd.Flags().BoolVar(&all, "all", false, "Include all issues (default: open only)")
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

// BuildAutoStatusesCmd creates the top-level "statuses" command that auto-detects the tracker.
func BuildAutoStatusesCmd(deps cmdutil.Deps) *cobra.Command {
	var table bool

	cmd := &cobra.Command{
		Use:   "statuses KEY_OR_URL",
		Short: "List available statuses for an issue (auto-detect tracker)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer result.Cleanup()

			if err := cmdprovider.RunListStatuses(cmd.Context(), result.Provider, cmd.OutOrStdout(), result.Key, table); err != nil {
				return err
			}

			project := tracker.ExtractProject(result.Key)
			PrintAutoHints(cmd.ErrOrStderr(), result.Kind, result.Key, project, "statuses")
			return nil
		},
	}
	cmd.Flags().BoolVar(&table, "table", false, "Output as human-readable table instead of JSON")
	return cmd
}

// BuildAutoStatusCmd creates the top-level "status" command that auto-detects the tracker.
func BuildAutoStatusCmd(deps cmdutil.Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "status KEY_OR_URL STATUS",
		Short: "Set the status of an issue (auto-detect tracker)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer result.Cleanup()

			if err := cmdprovider.RunSetStatus(cmd.Context(), result.Provider, cmd.OutOrStdout(), result.Key, args[1]); err != nil {
				return err
			}

			project := tracker.ExtractProject(result.Key)
			PrintAutoHints(cmd.ErrOrStderr(), result.Kind, result.Key, project, "status")
			return nil
		},
	}
}

// BuildAutoPRCreateCmd creates the top-level "pr create" command that derives
// the code forge and repository from the local git "origin" remote, so a PR
// can be opened from inside the repo without naming the forge kind or --repo.
func BuildAutoPRCreateCmd(deps cmdutil.Deps) *cobra.Command {
	var repo, base, head, title, body string

	prCmd := &cobra.Command{
		Use:   "pr",
		Short: "Pull request operations (auto-detect forge from git origin)",
	}

	createCmd := &cobra.Command{
		Use:     "create",
		Short:   "Open a pull request on the forge derived from the git origin remote",
		Example: `  human pr create --head fix-login --title "Fix login" --body "Closes #42"`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, originRepo, err := cmdutil.OriginForge(cmd, deps)
			if err != nil {
				return err
			}
			if repo == "" {
				repo = originRepo
			}
			return cmdprovider.RunCreatePullRequest(cmd.Context(), f, cmd.OutOrStdout(), repo, base, head, title, body)
		},
	}
	createCmd.Flags().StringVar(&repo, "repo", "", "Repository owner/repo (defaults to the git origin remote)")
	createCmd.Flags().StringVar(&head, "head", "", "Head branch holding the changes")
	_ = createCmd.MarkFlagRequired("head")
	createCmd.Flags().StringVar(&base, "base", "main", "Base branch to merge into")
	createCmd.Flags().StringVar(&title, "title", "", "Pull request title")
	_ = createCmd.MarkFlagRequired("title")
	createCmd.Flags().StringVar(&body, "body", "", "Pull request description in markdown")

	prCmd.AddCommand(createCmd)
	return prCmd
}

// PrintAutoHints prints contextual guidance to stderr after auto-detected commands.
func PrintAutoHints(w io.Writer, kind, key, project, afterCmd string) {
	_, _ = fmt.Fprintf(w, "\nDetected tracker: %s\n", kind)
	_, _ = fmt.Fprintln(w, "Related commands:")

	switch afterCmd {
	case "get":
		if project != "" {
			_, _ = fmt.Fprintf(w, "  human %s issues list --project=%s\n", kind, project)
		}
		if key != "" {
			_, _ = fmt.Fprintf(w, "  human %s issue  comment add %s 'text'\n", kind, key)
			_, _ = fmt.Fprintf(w, "  human %s issue  statuses %s\n", kind, key)
		}
	case "list":
		_, _ = fmt.Fprintf(w, "  human %s issue  get <KEY>\n", kind)
		if project != "" {
			_, _ = fmt.Fprintf(w, "  human %s issue  create --project=%s \"Title\" --description \"Description\"\n", kind, project)
		}
	case "statuses":
		if key != "" {
			_, _ = fmt.Fprintf(w, "  human %s issue  status %s \"<STATUS>\"\n", kind, key)
		}
	case "status":
		if key != "" {
			_, _ = fmt.Fprintf(w, "  human %s issue  statuses %s\n", kind, key)
		}
	}
}
