// Package cmdfeedback lets a person or an agent ask for the launch briefing
// the daemon would append to a stage's prompt (SC-6016).
//
// The daemon builds the briefing only for the launches it makes itself, so a
// run started by hand carries none. This command is the same answer on
// request: the block, or a plain "nothing recorded", from the same record,
// the same model call and the same cache a launch uses.
package cmdfeedback

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/daemon"
)

// connectDaemon is swapped in tests.
var connectDaemon = daemon.Connect

// noFeedback is the answer when the record has nothing for the stage. It is
// a full sentence on purpose: an empty output reads as a failed command.
const noFeedback = "No feedback for this stage."

// BuildFeedbackCmd builds `human feedback`.
func BuildFeedbackCmd() *cobra.Command {
	var (
		branch string
		raw    bool
	)
	cmd := &cobra.Command{
		Use:   "feedback KEY STAGE",
		Short: "What a launch of this stage would be told to be aware of, from past reviews",
		Long: "Print the briefing the daemon appends to a stage's launch prompt: a few lines\n" +
			"distilled from what past machine reviews found in this project, scoped to the\n" +
			"ticket and the stage. A run started by hand carries no such briefing; this is\n" +
			"how it asks for one.\n\n" +
			"STAGE is one of: " + strings.Join(daemon.FeedbackStageNames(), ", ") + ".\n" +
			"The pull-request stages scope by the branch's diff, so pass --branch for them.\n\n" +
			"--raw prints the prompt the briefing was distilled from — the scope files and\n" +
			"every record row the model saw — so a surprising line can be traced to its\n" +
			"finding. Needs the running daemon: the record and the model call live there.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			report, err := client.Feedback(daemon.FeedbackRequest{Key: args[0], Stage: daemon.BoardStage(args[1]), Branch: branch, Raw: raw})
			if err != nil {
				return err
			}
			render(cmd.OutOrStdout(), report, raw)
			return nil
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "Branch whose diff scopes the pull-request stages (prreview, prfix, deployfix)")
	cmd.Flags().BoolVar(&raw, "raw", false, "Also print the prompt the briefing was distilled from: the scope files and the record rows")
	return cmd
}

// render prints the briefing the way a launch prompt carries it — heading,
// then block — so what a person reads is byte-for-byte what an agent reads.
func render(w io.Writer, report daemon.FeedbackReport, raw bool) {
	var b strings.Builder
	if raw {
		fmt.Fprintf(&b, "# %s %s — %d record rows", report.Key, report.Stage, report.Rows)
		if report.Cached {
			b.WriteString(" (block from the launch cache)")
		}
		b.WriteString("\n")
		if len(report.Files) > 0 {
			b.WriteString("# scope: " + strings.Join(report.Files, " ") + "\n")
		}
		if report.Prompt != "" {
			b.WriteString("\n" + strings.TrimRight(report.Prompt, "\n") + "\n")
		}
		b.WriteString("\n")
	}
	if strings.TrimSpace(report.Block) == "" {
		b.WriteString(noFeedback + "\n")
	} else {
		b.WriteString(daemon.FeedbackHeading + "\n\n" + strings.TrimRight(report.Block, "\n") + "\n")
	}
	_, _ = io.WriteString(w, b.String())
}
