// Package cmdfsm lets something inside the pipeline ask the state machine where
// it is and what it may do.
//
// `where` is the command with the value, and the shape of everything else
// follows from why: the one fact an agent reliably has is its own ticket key. It
// does not know which state it is in — that is what it is asking — so a surface
// that made it name a state first would answer only the question it could
// already answer. `marker` and `constants` are here because they are likewise
// answerable from what an agent holds: the marker it is about to post, and no
// context at all.
package cmdfsm

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/pipelinefsm"
)

// defaultActor is who is asking when nobody says. An agent following a prompt is
// the overwhelmingly common caller, and it is the safe default: `skill` owns the
// fewest edges, so guessing it withholds a command rather than offering one the
// caller may not use.
const defaultActor = "skill"

// resolveDaemon is swapped in tests. `where` is the only subcommand that needs a
// daemon; the rest read the compiled-in document and work in any container.
var resolveDaemon = daemon.ResolveDaemon

// BuildFSMCmd builds `human fsm`.
func BuildFSMCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fsm",
		Short: "Ask the pipeline state machine where an item is and who may move it",
		Long: "Ask the pipeline state machine.\n\n" +
			"`where` needs the running daemon, because where an item IS depends on its\n" +
			"agents' liveness and its spent retries, which only the daemon holds. The\n" +
			"others read the machine compiled into this binary and work anywhere.",
	}
	cmd.AddCommand(buildWhereCmd(), buildMarkerCmd(), buildConstantsCmd())
	return cmd
}

// emit writes v as indented JSON. One renderer, so every answer has one shape
// and one test surface.
func emit(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// The commands carry angle-bracket placeholders, and Go escapes those by
	// default. A caller pastes the string it was handed, so the escaped form
	// would reach a shell. Nothing here is ever rendered as HTML.
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func load() (pipelinefsm.Document, error) {
	doc, err := pipelinefsm.Load()
	if err != nil {
		return pipelinefsm.Document{}, errors.WrapWithDetails(err, "reading the compiled-in state machine", "doc", pipelinefsm.DocPath)
	}
	return doc, nil
}

func buildWhereCmd() *cobra.Command {
	var actor string
	cmd := &cobra.Command{
		Use:   "where KEY",
		Short: "Where an item is, what holds there, and which ways out are yours",
		Long: "Report where a ticket is in the pipeline: the state it is in, what must be\n" +
			"true while it sits there, who may move it, what happens if nobody does, and\n" +
			"every way out — with a runnable command only for the ways out that are yours.\n\n" +
			"When the evidence cannot identify a single state, the candidates are listed\n" +
			"with the reason. That is deliberate: acting on a state you are not in means\n" +
			"taking an edge you do not own.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, token, err := resolveDaemon()
			if err != nil {
				return err
			}
			report, err := daemon.FSMWhere(addr, token, daemon.WhereRequest{Key: args[0], Actor: actor})
			if err != nil {
				return err
			}
			return emit(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().StringVar(&actor, "actor", defaultActor, "Who is asking (user, daemon, skill) — only this actor's ways out carry a command")
	return cmd
}

func buildMarkerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "marker NAME",
		Short: "What a marker records, where posting it moves an item, what it requires",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := load()
			if err != nil {
				return err
			}
			name := pipelinefsm.MarkerType(args[0])
			uses := doc.MarkerUses(name)
			listed, dualRole := doc.RecordsContentOnly(name)
			if len(uses) == 0 && !listed {
				return errors.WithDetails("the state machine does not mention this marker",
					"marker", name,
					"hint", "human marker post also accepts open-ended types the machine does not model")
			}

			moves := make([]map[string]any, 0, len(uses))
			for _, e := range uses {
				moves = append(moves, map[string]any{
					"event":         e.Name,
					"from":          e.Src,
					"to":            e.Dst,
					"actor":         e.Actor,
					"to_is_derived": e.DstIsDerived,
					"doc":           e.Doc,
				})
			}
			return emit(cmd.OutOrStdout(), map[string]any{
				"document_version": doc.Version,
				"marker":           pipelinefsm.MarkerHeader(name),
				"type":             name,
				"required_fields":  marker.RequiredFields(name),
				"moves_an_item":    len(uses) > 0,
				"moves":            moves,
				"records_content":  listed,
				"dual_role":        dualRole,
			})
		},
	}
}

func buildConstantsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "constants",
		Short: "The pipeline's real budgets — retries, graces, bounds",
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := load()
			if err != nil {
				return err
			}
			if len(doc.Invariants.Constants) == 0 {
				return fmt.Errorf("the compiled-in state machine declares no constants")
			}
			return emit(cmd.OutOrStdout(), map[string]any{
				"document_version": doc.Version,
				"constants":        doc.Invariants.Constants,
			})
		},
	}
}
