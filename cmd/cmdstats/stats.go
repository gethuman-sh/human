// Package cmdstats implements the "human stats" command tree, which reads the
// rolling activity record the daemon keeps. The daemon owns the stats database,
// so every read is forwarded to it.
package cmdstats

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/stats"
)

// noDaemonMsg is shown when the daemon is unreachable. Activity is recorded
// only by the daemon, so without it there is nothing to read.
const noDaemonMsg = "activity statistics are recorded by the daemon; start it with `human daemon start`"

// notCapturedLabel names an empty model in the table. An empty cell would read
// as a rendering gap; this says what it means — the row predates capture, or the
// event never carried an attribution.
const notCapturedLabel = "(not captured)"

// validRanges are the windows the daemon's stats routes accept. 30d is also the
// retention ceiling for this data, so nothing longer would have rows to return.
var validRanges = []string{"24h", "7d", "30d"}

// BuildStatsCmd creates the "stats" command tree.
func BuildStatsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "stats",
		Short:   "Inspect the rolling record of AI agent activity",
		GroupID: "utility",
	}
	// No GroupID on the subcommand: cobra panics when a GroupID names a group
	// its parent has not registered, and "stats" registers none.
	cmd.AddCommand(buildSubagentsCmd())
	return cmd
}

func buildSubagentsCmd() *cobra.Command {
	var (
		rng    string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "subagents",
		Short: "Show which sub-agent types ran on which model over a time range",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isValidRange(rng) {
				return errors.WithDetails("unknown range", "range", rng, "valid", validRanges)
			}
			client, err := connectDaemon(cmd.OutOrStdout())
			if err != nil {
				return nil // guidance already printed; an unreachable daemon is not a failure
			}
			counts, err := client.QuerySubagentModels(rng)
			if err != nil {
				return errors.WrapWithDetails(err, "failed to query sub-agent statistics")
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), counts)
			}
			return renderTable(cmd.OutOrStdout(), counts)
		},
	}
	cmd.Flags().StringVar(&rng, "range", "7d", "time window: 24h, 7d or 30d")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw JSON")
	return cmd
}

// isValidRange rejects an unknown window up front. The daemon silently falls
// back to 24h for one, which would answer a different question than the one
// asked without saying so.
func isValidRange(rng string) bool {
	for _, v := range validRanges {
		if rng == v {
			return true
		}
	}
	return false
}

// connect is the daemon discovery path, held in a variable so tests can decide
// whether a daemon exists. Discovery falls back to host.docker.internal, so a
// test that relies on the ambient environment passes or fails according to
// whether a real daemon happens to be running beside it.
var connect = daemon.Connect

// connectDaemon returns a client for the running daemon. When there is none it
// prints guidance to out and returns an error so the caller can stop early
// without reporting a missing daemon as a command failure.
func connectDaemon(out io.Writer) (*daemon.Client, error) {
	c, err := connect()
	if err != nil {
		_, _ = fmt.Fprintln(out, noDaemonMsg)
		return nil, err
	}
	return c, nil
}

func writeJSON(out io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errors.WrapWithDetails(err, "marshal sub-agent stats JSON")
	}
	_, _ = fmt.Fprintln(out, string(data))
	return nil
}

// renderTable prints one line per sub-agent type and model pairing.
func renderTable(out io.Writer, counts []stats.SubagentModelCount) error {
	if len(counts) == 0 {
		_, _ = fmt.Fprintln(out, "no sub-agent spawns recorded in this range")
		return nil
	}
	_, _ = fmt.Fprintf(out, "%-32s  %-20s  %s\n", "SUB-AGENT TYPE", "MODEL", "SPAWNS")
	for _, c := range counts {
		_, _ = fmt.Fprintf(out, "%-32s  %-20s  %d\n", c.SubagentType, modelLabel(c.Model), c.Count)
	}
	return nil
}

// modelLabel renders the two non-model cases as words rather than as blanks, so
// "ran on the parent's model" and "no attribution was ever recorded" stay
// distinguishable on screen and not only in the database (SC-3582).
func modelLabel(model string) string {
	switch model {
	case "":
		return notCapturedLabel
	case hookevents.ModelInherited:
		return hookevents.ModelInherited
	default:
		return model
	}
}
