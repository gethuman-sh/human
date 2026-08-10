// Package cmdbug provides the `human bug` and `human security` commands: file a
// defect or security ticket auto-scoped to the configured PM group, hitting the
// same daemon routes the desktop board's + buttons use.
package cmdbug

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
)

// BuildBugCmd builds the top-level "bug" command with a "create" subcommand.
func BuildBugCmd() *cobra.Command {
	bugCmd := &cobra.Command{
		Use:   "bug",
		Short: "Defect-ticket operations",
	}
	var description string
	createCmd := &cobra.Command{
		Use:   "create TITLE",
		Short: "File a bug on the configured PM group (auto-scoped, bug-typed — no --project, no --type)",
		Example: `  human bug create "Login button does nothing on Safari"
  human bug create "Crash on empty query" --description "Steps: open search, hit enter with no text"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunBugCreate(cmd.OutOrStdout(), args[0], description)
		},
	}
	createCmd.Flags().StringVar(&description, "description", "", "Bug description in markdown (separate from title)")
	bugCmd.AddCommand(createCmd)
	return bugCmd
}

// BuildSecurityCmd builds the top-level "security" command with a "create" subcommand.
func BuildSecurityCmd() *cobra.Command {
	securityCmd := &cobra.Command{
		Use:   "security",
		Short: "Security-ticket operations",
	}
	var description string
	createCmd := &cobra.Command{
		Use:     "create TITLE",
		Short:   "File a security ticket on the configured PM group (auto-scoped — no --project, no --type)",
		Example: `  human security create "Auth token leaks in error logs"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunSecurityCreate(cmd.OutOrStdout(), args[0], description)
		},
	}
	createCmd.Flags().StringVar(&description, "description", "", "Security-ticket description in markdown (separate from title)")
	securityCmd.AddCommand(createCmd)
	return securityCmd
}

// RunBugCreate files a bug via the daemon's bug-create route — the same code
// path as the desktop board's Bugs-pane + button — and prints "<key>\t<url>".
func RunBugCreate(out io.Writer, title, description string) error {
	if strings.TrimSpace(title) == "" {
		return errors.WithDetails("bug title must not be empty")
	}
	client, err := connectDaemon()
	if err != nil {
		return err
	}
	resp, err := client.BugCreate(daemon.BugCreateRequest{
		Title:       title,
		Description: description,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\t%s\n", resp.Key, resp.URL)
	return err
}

// RunSecurityCreate files a security ticket via the daemon's security-create route.
func RunSecurityCreate(out io.Writer, title, description string) error {
	if strings.TrimSpace(title) == "" {
		return errors.WithDetails("security ticket title must not be empty")
	}
	client, err := connectDaemon()
	if err != nil {
		return err
	}
	resp, err := client.SecurityCreate(daemon.SecurityCreateRequest{
		Title:       title,
		Description: description,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\t%s\n", resp.Key, resp.URL)
	return err
}

// connectDaemon is swapped in tests, which is the only reason it is a var: a
// devcontainer or CI host answers on the well-known fallback address, so a test
// that wants the no-daemon case has to be able to say so.
var connectDaemon = daemon.Connect
