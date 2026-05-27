package cmdutil

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/tracker"
)

// SetupHelp overrides the root command's help function to append examples
// and connected trackers when showing root-level help.
func SetupHelp(rootCmd *cobra.Command, loader func() ([]tracker.Instance, error)) {
	defaultHelp := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		defaultHelp(cmd, args)
		// Only append extras for root-level help.
		if cmd != rootCmd {
			return
		}
		w := cmd.OutOrStdout()
		PrintExamples(w)
		PrintConnectedTrackers(w, loader)
	})
}

// PrintConnectedTrackers appends a "Connected trackers:" section to the help
// output.  Errors are silently ignored so that help always works.
func PrintConnectedTrackers(w io.Writer, loader func() ([]tracker.Instance, error)) {
	instances, err := loader()
	if err != nil {
		return
	}
	if len(instances) == 0 {
		_, _ = fmt.Fprintln(w, "Connected trackers: none")
		_, _ = fmt.Fprintln(w, "  Configure trackers in .humanconfig.yaml")
		return
	}
	_, _ = fmt.Fprintln(w, "Connected trackers:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, inst := range instances {
		line := fmt.Sprintf("  %s\t%s\t%s", inst.Name, inst.Kind, inst.URL)
		if inst.User != "" {
			line += "\t" + inst.User
		}
		if inst.Description != "" {
			line += "\t" + inst.Description
		}
		_, _ = fmt.Fprintln(tw, line)
	}
	_ = tw.Flush()
}

// PrintExamples prints the quick command and provider examples.
func PrintExamples(w io.Writer) {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Quick commands (auto-detect tracker):")
	_, _ = fmt.Fprintln(w, "  human get KAN-1")
	_, _ = fmt.Fprintln(w, "  human list --project=KAN")
	_, _ = fmt.Fprintln(w, "  human list --project=KAN --tracker=work")
	_, _ = fmt.Fprintln(w, "  human statuses KAN-1")
	_, _ = fmt.Fprintln(w, `  human status KAN-1 "Done"`)
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Command pattern:")
	_, _ = fmt.Fprintln(w, "  human <tracker> issues list --project=<PROJECT>   List issues (JSON)")
	_, _ = fmt.Fprintln(w, "  human <tracker> issue  get <KEY>                  Get issue (markdown)")
	_, _ = fmt.Fprintln(w, `  human <tracker> issue  create --project=<P> "Title" --description "Details"`)
	_, _ = fmt.Fprintln(w, `  human <tracker> issue  edit <KEY> --title "New" --description "Updated"`)
	_, _ = fmt.Fprintln(w, "  human <tracker> issue  delete <KEY>               Show confirmation code")
	_, _ = fmt.Fprintln(w, "  human <tracker> issue  delete <KEY> --confirm=N   Delete/close issue")
	_, _ = fmt.Fprintln(w, "  human <tracker> issue  start <KEY>                Start working on issue")
	_, _ = fmt.Fprintln(w, "  human <tracker> issue  statuses <KEY>             List available statuses")
	_, _ = fmt.Fprintln(w, `  human <tracker> issue  status <KEY> "<STATUS>"    Set issue status`)
	_, _ = fmt.Fprintln(w, "  human <tracker> issue  comment add <KEY> <BODY>   Add comment")
	_, _ = fmt.Fprintln(w, "  human <tracker> issue  comment list <KEY>         List comments")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Project key and issue key formats by tracker:")
	_, _ = fmt.Fprintln(w, "  jira        --project=KAN                  issue key: KAN-1")
	_, _ = fmt.Fprintln(w, "  github      --project=octocat/hello-world  issue key: octocat/hello-world#42")
	_, _ = fmt.Fprintln(w, "  gitlab      --project=mygroup/myproject    issue key: mygroup/myproject#42")
	_, _ = fmt.Fprintln(w, "  linear      --project=ENG                  issue key: ENG-123")
	_, _ = fmt.Fprintln(w, "  azuredevops --project=MyProject             issue key: 42")
	_, _ = fmt.Fprintln(w, "  shortcut    --project=MyProject             issue key: 123")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Examples:")
	_, _ = fmt.Fprintln(w, "  human jira issues list --project=KAN")
	_, _ = fmt.Fprintln(w, "  human jira issue get KAN-1")
	_, _ = fmt.Fprintln(w, `  human jira issue create --project=KAN "Implement login page" --description "Add OAuth2 login flow with Google provider"`)
	_, _ = fmt.Fprintln(w, "  human github issues list --project=octocat/hello-world")
	_, _ = fmt.Fprintln(w, "  human github issue get octocat/hello-world#42")
	_, _ = fmt.Fprintln(w, `  human jira issue edit KAN-1 --title "Updated title"`)
	_, _ = fmt.Fprintln(w, "  human jira issue start KAN-1")
	_, _ = fmt.Fprintln(w, "  human jira issue statuses KAN-1")
	_, _ = fmt.Fprintln(w, `  human jira issue status KAN-1 "Done"`)
	_, _ = fmt.Fprintln(w, "  human jira issue delete KAN-1                    # shows confirmation code")
	_, _ = fmt.Fprintln(w, "  human jira issue delete KAN-1 --confirm=4521     # deletes")
	_, _ = fmt.Fprintln(w, "  human jira issue comment add KAN-1 'Looks good'")
	_, _ = fmt.Fprintln(w, "  human notion search \"quarterly report\"")
	_, _ = fmt.Fprintln(w, "  human notion page get <page-id>")
	_, _ = fmt.Fprintln(w, "  human figma file get <file-key>")
	_, _ = fmt.Fprintln(w, "  human figma file comments <file-key>")
	_, _ = fmt.Fprintln(w, "  human amplitude events list")
	_, _ = fmt.Fprintln(w, "  human amplitude cohorts list")
	_, _ = fmt.Fprintln(w, "  human telegram list")
	_, _ = fmt.Fprintln(w, "  human telegram get 123456789")
	_, _ = fmt.Fprintln(w, "  human index                                       Build search index from all trackers and Notion")
	_, _ = fmt.Fprintln(w, "  human index --source=notion                       Index only Notion pages and databases")
	_, _ = fmt.Fprintln(w, "  human index --status                              Show index statistics")
	_, _ = fmt.Fprintln(w, `  human search "retry logic"                         Search the index`)
	_, _ = fmt.Fprintln(w, `  human search "auth spec" --source=notion           Search only Notion entries`)
	_, _ = fmt.Fprintln(w, "  human tracker list")
	_, _ = fmt.Fprintln(w, "  human init")
	_, _ = fmt.Fprintln(w, "  human install --agent claude")
	_, _ = fmt.Fprintln(w, "  human browser https://example.com")
	_, _ = fmt.Fprintln(w, "  human daemon start")
	_, _ = fmt.Fprintln(w, "  human daemon stop")
	_, _ = fmt.Fprintln(w, "  human daemon token")
	_, _ = fmt.Fprintln(w, "  human daemon status")
}
