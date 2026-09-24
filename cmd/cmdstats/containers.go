package cmdstats

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
)

func buildContainersCmd() *cobra.Command {
	var (
		rng    string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "containers",
		Short: "Show what agent containers cost the machine, per stage, against the engine's ceiling",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isValidRange(rng) {
				return errors.WithDetails("unknown range", "range", rng, "valid", validRanges)
			}
			client, err := connectDaemon(cmd.OutOrStdout())
			if err != nil {
				return nil // guidance already printed; an unreachable daemon is not a failure
			}
			report, err := client.QueryContainerResources(rng)
			if err != nil {
				return errors.WrapWithDetails(err, "failed to query container statistics")
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			renderContainers(cmd.OutOrStdout(), report)
			return nil
		},
	}
	cmd.Flags().StringVar(&rng, "range", "7d", "time window: 24h, 7d or 30d")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw JSON")
	return cmd
}

// renderContainers prints the engine's ceiling first, then one line per stage.
// The ceiling comes first because every number below it is only meaningful
// against it: 1.9 GB is fine on a 64 GB host and fatal on a 2 GB VM.
func renderContainers(out io.Writer, report daemon.ContainerResourceReport) {
	if report.Engine.Known {
		_, _ = fmt.Fprintf(out, "engine: %d CPUs, %s memory\n", report.Engine.NCPU, formatBytes(uint64(report.Engine.MemTotalBytes))) // #nosec G115 -- engine memory is never negative
	} else {
		_, _ = fmt.Fprintln(out, "engine: capacity unknown (engine not reachable)")
	}
	if len(report.Stages) == 0 {
		_, _ = fmt.Fprintln(out, "no container samples recorded in this range")
		return
	}
	_, _ = fmt.Fprintf(out, "%-16s  %5s  %-22s  %8s  %8s  %s\n", "STAGE", "RUNS", "PEAK MEM / LIMIT", "AVG CPU", "PEAK CPU", "OOM KILLS")
	for _, s := range report.Stages {
		_, _ = fmt.Fprintf(out, "%-16s  %5d  %-22s  %7.0f%%  %7.0f%%  %d\n",
			s.Stage, s.Runs, formatBytes(s.PeakMemBytes)+" / "+formatBytes(s.MemLimitBytes), s.AvgCPUPercent, s.PeakCPUPercent, s.OOMKills)
	}
}

// formatBytes renders a byte count in the unit a person would pick.
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
