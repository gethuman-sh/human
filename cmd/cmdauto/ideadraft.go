package cmdauto

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/ideadraft"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// IdeaDraftOpts selects which of the three things `human idea draft` does.
// Exactly one is set per run; the command rejects any other combination,
// because a run that both checks and writes has no honest verdict to report.
type IdeaDraftOpts struct {
	// DescriptionFile holds the drafted text, or "-" for stdin.
	DescriptionFile string
	// Check reports the verdict and changes nothing at all.
	Check bool
	// StandDown pins the current description as human-authored, so nothing
	// automatic ever redrafts this ticket again.
	StandDown bool
	// Stdin is where "-" reads from; nil means os.Stdin.
	Stdin io.Reader
}

// ideaDraftResult is the one JSON object the command prints. It is a machine
// handoff: the drafting agent reads `decision` to know whether its work was
// wanted, and `roundtrip_ok` to know whether the tracker stored the [TBA:]
// text it wrote verbatim.
type ideaDraftResult struct {
	Key         string `json:"key"`
	Decision    string `json:"decision"`
	Reason      string `json:"reason"`
	Written     bool   `json:"written"`
	TBA         int    `json:"tba"`
	RoundtripOK bool   `json:"roundtrip_ok"`
}

// buildIdeaDraftCmd adds `human idea draft KEY`, the ONLY write path a
// background drafter has. The guard lives here rather than in the drafting
// prompt because a prompt cannot be tested and this decision is the one that
// protects the user's own words.
func buildIdeaDraftCmd(deps cmdutil.Deps) *cobra.Command {
	var opts IdeaDraftOpts
	cmd := &cobra.Command{
		Use:   "draft KEY_OR_URL",
		Short: "Write a machine-drafted description, but only over the machine's own words",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateIdeaDraftOpts(opts); err != nil {
				return err
			}
			result, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer result.Cleanup()
			return RunIdeaDraft(cmd.Context(), result.Provider, cmd.OutOrStdout(), result.Key, opts)
		},
	}
	cmd.Flags().StringVar(&opts.DescriptionFile, "description-file", "", "File holding the new description, or - for stdin")
	cmd.Flags().BoolVar(&opts.Check, "check", false, "Report the verdict without writing or recording anything")
	cmd.Flags().BoolVar(&opts.StandDown, "stand-down", false, "Record that the description is human-authored and must never be redrafted")
	return cmd
}

// validateIdeaDraftOpts refuses any combination whose outcome would be
// ambiguous — the modes are exclusive and one must be chosen.
func validateIdeaDraftOpts(opts IdeaDraftOpts) error {
	modes := 0
	for _, on := range []bool{opts.Check, opts.StandDown, opts.DescriptionFile != ""} {
		if on {
			modes++
		}
	}
	if modes == 0 {
		return errors.WithDetails("choose one of --check, --stand-down or --description-file",
			"modes", "check|stand-down|description-file")
	}
	if modes > 1 {
		return errors.WithDetails("--check, --stand-down and --description-file are exclusive",
			"modes", "check|stand-down|description-file")
	}
	return nil
}

// RunIdeaDraft fetches the ticket, asks the guard, and only then writes.
//
// A stand-down is an OUTCOME, not a failure: the command exits 0 and says so in
// its JSON, because a drafter that reports failure for "a human owns this now"
// teaches its caller to retry the one thing it must never do.
func RunIdeaDraft(ctx context.Context, p tracker.Provider, out io.Writer, key string, opts IdeaDraftOpts) error {
	issue, err := p.GetIssue(ctx, key)
	if err != nil {
		return err
	}
	comments, err := p.ListComments(ctx, key)
	if err != nil {
		return err
	}
	verdict, reason := ideadraft.Decide(issue.IsIdea(), issue.Title, issue.Description, comments)
	res := ideaDraftResult{Key: key, Decision: string(verdict), Reason: reason, TBA: ideadraft.TBACount(issue.Description)}

	switch {
	case opts.Check:
	case opts.StandDown:
		if err := recordStandDown(ctx, p, key, issue.Description, verdict, comments); err != nil {
			return err
		}
	case verdict == ideadraft.VerdictWrite:
		if err := writeIdeaDraft(ctx, p, key, issue.Title, opts, &res); err != nil {
			return err
		}
	}
	return printIdeaDraftResult(out, res)
}

// recordStandDown pins the current description as the human's. It is
// idempotent: a ticket already carrying a human record for these exact bytes
// gains nothing from a second one.
func recordStandDown(ctx context.Context, p tracker.Provider, key, description string, verdict ideadraft.Verdict, comments []tracker.Comment) error {
	if verdict != ideadraft.VerdictStandDown {
		return nil
	}
	prev := ideadraft.LatestProvenance(comments)
	if prev.Found && prev.Author == ideadraft.AuthorHuman && prev.Description == ideadraft.Fingerprint(description) {
		return nil
	}
	_, err := p.AddComment(ctx, key, marker.Render(ideadraft.HumanRecord(description), ideadraft.FieldOrder))
	return err
}

// writeIdeaDraft writes the description and records the provenance, then reads
// the ticket back: what cannot be checked from code is whether the tracker's
// own editor preserves the literal [TBA: text, so the first real run answers it
// instead of the assumption failing silently.
func writeIdeaDraft(ctx context.Context, p tracker.Provider, key, title string, opts IdeaDraftOpts, res *ideaDraftResult) error {
	text, err := readIdeaDraftText(opts)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.WithDetails("refusing to write an empty description", "key", key)
	}
	if _, err := p.EditIssue(ctx, key, tracker.EditOptions{Description: &text}); err != nil {
		return err
	}
	if _, err := p.AddComment(ctx, key, marker.Render(ideadraft.MachineRecord(text, title), ideadraft.FieldOrder)); err != nil {
		return err
	}
	res.Written = true
	res.TBA = ideadraft.TBACount(text)
	after, err := p.GetIssue(ctx, key)
	res.RoundtripOK = err == nil && after != nil && ideadraft.TBACount(after.Description) == res.TBA
	return nil
}

func readIdeaDraftText(opts IdeaDraftOpts) (string, error) {
	if opts.DescriptionFile == "-" {
		in := opts.Stdin
		if in == nil {
			in = os.Stdin
		}
		b, err := io.ReadAll(in)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := os.ReadFile(opts.DescriptionFile)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func printIdeaDraftResult(out io.Writer, res ideaDraftResult) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}
