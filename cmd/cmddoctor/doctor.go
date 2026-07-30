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
				// An unreachable daemon is a genuine hard stop — nothing can run.
				printCheck(out, daemon.DoctorCheck{Name: "daemon", OK: false, Severity: daemon.SeverityBlocking}, "not reachable — start it with 'human daemon'")
				return errors.WithDetails("daemon not reachable")
			}
			printCheck(out, daemon.DoctorCheck{Name: "daemon", OK: true}, "reachable at "+info.Addr)

			data, err := daemon.GetDoctor(info.Addr, info.Token, refresh)
			if err != nil {
				return errors.WrapWithDetails(err, "querying daemon doctor")
			}
			for _, c := range data.Checks {
				printCheck(out, c, c.Detail)
			}
			// Only a genuinely blocked substrate is a hard stop. A failing
			// non-gating or momentary check is reported as it happened —
			// visibly, but never as an outage that halts launches (SC-1991).
			if data.Blocked {
				return errors.WithDetails("substrate blocked — " + data.Summary)
			}
			summary := data.Summary
			if summary == "" {
				summary = "all systems go"
			}
			_, _ = fmt.Fprintf(out, "\n%s\n", summary)
			return nil
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "force a live check run instead of the cached result")
	return cmd
}

// printCheck renders one check line. A passing check gets a check mark; a
// failing one is marked by its real consequence — a blocking failure is loud
// (✗), while a merely advisory or momentary one is a softer note (!) so the
// output does not read every failure as a hard stop (SC-1991).
func printCheck(out io.Writer, c daemon.DoctorCheck, detail string) {
	mark := "✓"
	if !c.OK {
		switch c.Severity {
		case daemon.SeverityBlocking:
			mark = "✗"
		default:
			mark = "!"
		}
	}
	if detail != "" {
		detail = " — " + detail
	}
	_, _ = fmt.Fprintf(out, "%s %s%s\n", mark, c.Name, detail)
}
