// Package cmdcapabilities surfaces the run's capability set as
// `human capabilities`, so a pipeline agent can ask what it may do instead of
// carrying a branch per execution context in its prompt.
package cmdcapabilities

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/capabilities"
)

// BuildCapabilitiesCmd creates the "capabilities" command.
func BuildCapabilitiesCmd() *cobra.Command {
	return buildCapabilitiesCmd(capabilities.GitRemoteProbe)
}

func buildCapabilitiesCmd(probe capabilities.RemoteProbe) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Report what this run may do (push, open a PR, deploy)",
		Long: `Report what this run may do.

Pipeline agents read this instead of branching on their execution context.
The rule is one line: attempt nothing the capability set forbids, and treat a
missing capability as a boundary, never as a failure.

It runs locally rather than on the daemon: the answer describes the caller's
own checkout and environment, which is exactly what the daemon cannot see.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set := capabilities.Detect(cmd.Context(), probe)
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(set)
			}
			return writeText(cmd.OutOrStdout(), set)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the capability set as JSON")
	return cmd
}

func writeText(out io.Writer, set capabilities.Set) error {
	rows := []struct {
		name string
		ok   bool
	}{
		{"push", set.CanPush},
		{"open-pr", set.CanOpenPR},
		{"deploy", set.OwnsDeploy},
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(out, "%-10s %s\n", r.name, yesNo(r.ok)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "%-10s %s\n", "workspace", set.Workspace); err != nil {
		return err
	}
	if set.Reason == "" {
		return nil
	}
	_, err := fmt.Fprintf(out, "\n%s\n", set.Reason)
	return err
}

func yesNo(ok bool) string {
	if ok {
		return "yes"
	}
	return "no"
}
