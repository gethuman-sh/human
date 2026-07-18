// Package cmddoctor implements `human doctor`: the preflight health report
// for the agent pipeline's substrate. Infrastructure failures must be
// attributed to infrastructure — the doctor names what is broken and how to
// fix it, before an agent run burns minutes rediscovering it.
package cmddoctor

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
)

// BuildDoctorCmd creates the doctor command.
func BuildDoctorCmd() *cobra.Command {
	var refresh bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the health of the agent pipeline's substrate",
		Long: "Runs the daemon's preflight checks (tracker credentials, docker, proxy CA,\n" +
			"agent skills, persistence) and prints each with its fix. The board's status\n" +
			"LED shows the same result.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			info, err := daemon.ReadInfo()
			if err != nil || !info.IsReachable() {
				printCheck(out, false, "daemon", "not reachable — start it with 'human daemon'")
				return errors.WithDetails("daemon not reachable")
			}
			printCheck(out, true, "daemon", "reachable at "+info.Addr)

			data, err := daemon.GetDoctor(info.Addr, info.Token, refresh)
			if err != nil {
				return errors.WrapWithDetails(err, "querying daemon doctor")
			}
			for _, c := range data.Checks {
				printCheck(out, c.OK, c.Name, c.Detail)
			}
			if !data.Healthy {
				return errors.WithDetails("substrate unhealthy — agent launches on failing checks are blocked")
			}
			_, _ = fmt.Fprintln(out, "\nAll systems go.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "force a live check run instead of the cached result")
	return cmd
}

func printCheck(out io.Writer, ok bool, name, detail string) {
	mark := "✓"
	if !ok {
		mark = "✗"
	}
	if detail != "" {
		detail = " — " + detail
	}
	_, _ = fmt.Fprintf(out, "%s %s%s\n", mark, name, detail)
}
