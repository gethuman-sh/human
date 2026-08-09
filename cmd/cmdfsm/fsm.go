// Package cmdfsm answers questions about the pipeline state machine the binary
// carries.
//
// Every command here reads compiled-in bytes: no daemon, no credentials, no
// checkout. That is the point rather than an optimisation — the asker is
// usually an agent in a container on some other project, and a question it can
// only ask from the host is a question it cannot ask.
package cmdfsm

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/pipelinefsm"
)

// defaultActor is who is asking when nobody says. An agent following a prompt
// is the overwhelmingly common caller, and it is also the safe default: `skill`
// owns the fewest edges, so guessing it withholds a command rather than
// offering one the caller may not use.
const defaultActor = "skill"

// BuildFSMCmd builds `human fsm`.
func BuildFSMCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fsm",
		Short: "Ask the pipeline state machine where an item can go and who may move it",
		Long: "Ask the pipeline state machine the binary carries.\n\n" +
			"Answers come from the compiled-in document, so they are the same inside an\n" +
			"agent container as on the host, and need no daemon and no credentials.",
	}
	cmd.AddCommand(buildStatesCmd(), buildShowCmd(), buildNextCmd(), buildMarkerCmd(), buildConstantsCmd())
	return cmd
}

// emit writes v as indented JSON. One renderer, so every command has one output
// shape and one test surface.
func emit(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// The commands carry angle-bracket placeholders, and Go escapes those to
	// <…> by default. A caller pastes the string it was handed, so the
	// escaped form would reach a shell. Nothing here is ever rendered as HTML.
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// load is the shared entry so a broken compiled-in document fails the same way
// everywhere rather than once per command.
func load() (pipelinefsm.Document, error) {
	doc, err := pipelinefsm.Load()
	if err != nil {
		return pipelinefsm.Document{}, errors.WrapWithDetails(err, "reading the compiled-in state machine", "doc", pipelinefsm.DocPath)
	}
	return doc, nil
}

// stateOr fails with the list of states the caller could have named. A wrong
// state name is the likeliest mistake here, and an error that only says "not
// found" makes the caller guess a second time.
func stateOr(doc pipelinefsm.Document, name string) (pipelinefsm.State, error) {
	s, ok := doc.FindState(name)
	if !ok {
		return pipelinefsm.State{}, errors.WithDetails("no such state in the pipeline state machine",
			"state", name, "known", strings.Join(doc.StateNames(), ", "))
	}
	return s, nil
}

type stateSummary struct {
	Name       string                     `json:"name"`
	Doc        string                     `json:"doc,omitempty"`
	Board      pipelinefsm.BoardPlacement `json:"board"`
	Terminal   bool                       `json:"terminal,omitempty"`
	Reopenable bool                       `json:"reopenable,omitempty"`
}

func buildStatesCmd() *cobra.Command {
	var stage string
	cmd := &cobra.Command{
		Use:   "states",
		Short: "List every state, with where an item in it sits on the board",
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := load()
			if err != nil {
				return err
			}
			out := make([]stateSummary, 0, len(doc.States))
			for _, s := range doc.States {
				// "any" and "none" are not stages; a stage filter that matched them
				// would answer a question the caller did not ask.
				if stage != "" && s.Board.Stage != stage {
					continue
				}
				out = append(out, stateSummary{s.Name, s.Doc, s.Board, s.Terminal, s.Reopenable})
			}
			return emit(cmd.OutOrStdout(), map[string]any{
				"document_version": doc.Version,
				"initial":          doc.Initial,
				"states":           out,
			})
		},
	}
	cmd.Flags().StringVar(&stage, "stage", "", "Only states whose board stage is this (backlog, planning, implementation, verification, done)")
	return cmd
}

func buildShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show STATE",
		Short: "What must hold in a state, who may act, and what happens if nobody does",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := load()
			if err != nil {
				return err
			}
			s, err := stateOr(doc, args[0])
			if err != nil {
				return err
			}
			return emit(cmd.OutOrStdout(), map[string]any{
				"document_version":   doc.Version,
				"name":               s.Name,
				"doc":                s.Doc,
				"board":              s.Board,
				"terminal":           s.Terminal,
				"reopenable":         s.Reopenable,
				"holds":              s.Holds,
				"who_may_act":        s.WhoMayAct,
				"stale_when":         s.StaleWhen,
				"if_nothing_happens": s.IfNothingHappens,
				"note":               s.Note,
			})
		},
	}
}

// wayOut is one transition leaving a state, as the caller needs to see it.
//
// Yours and Command are the load-bearing pair. A caller sees every way out —
// that is how it knows what waiting buys it and who it is waiting for — but only
// its own carry something it can run. An edge with no command is not an edge to
// improvise: posting its marker yourself puts the item in a state nobody drove
// it to, which is precisely how [human:deployed] would get posted by an agent
// that merely finished its work.
type wayOut struct {
	Event   string `json:"event"`
	To      string `json:"to"`
	Actor   string `json:"actor"`
	Marker  string `json:"marker,omitempty"`
	Doc     string `json:"doc,omitempty"`
	Yours   bool   `json:"yours"`
	Command string `json:"command,omitempty"`

	// ToIsDerived says To is a placeholder: the real destination is computed
	// from the marker's body. Reported rather than hidden, so a caller does not
	// plan around a destination that was never a promise.
	ToIsDerived bool `json:"to_is_derived,omitempty"`

	// MovesItem is false for an edge that records something without moving the
	// item. A caller that treats it as progress would wait for a change that is
	// never coming.
	MovesItem bool `json:"moves_item"`
}

func buildNextCmd() *cobra.Command {
	var actor string
	cmd := &cobra.Command{
		Use:   "next STATE",
		Short: "Every way out of a state: what records it, who causes it, which are yours",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := load()
			if err != nil {
				return err
			}
			s, err := stateOr(doc, args[0])
			if err != nil {
				return err
			}
			if _, known := doc.Actors[actor]; !known {
				return errors.WithDetails("no such actor in the pipeline state machine",
					"actor", actor, "known", strings.Join(actorNames(doc), ", "))
			}
			ways := waysOut(doc, s.Name, actor)
			return emit(cmd.OutOrStdout(), map[string]any{
				"document_version":   doc.Version,
				"state":              s.Name,
				"asking_as":          actor,
				"who_may_act":        s.WhoMayAct,
				"if_nothing_happens": s.IfNothingHappens,
				"ways_out":           ways,
			})
		},
	}
	cmd.Flags().StringVar(&actor, "actor", defaultActor, "Who is asking (user, daemon, skill) — only this actor's ways out carry a command")
	return cmd
}

func actorNames(doc pipelinefsm.Document) []string {
	out := make([]string, 0, len(doc.Actors))
	for a := range doc.Actors {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// waysOut builds the answer for one state and one asker.
func waysOut(doc pipelinefsm.Document, state, actor string) []wayOut {
	events := doc.Out(state)
	out := make([]wayOut, 0, len(events))
	for _, e := range events {
		w := wayOut{
			Event:       e.Name,
			To:          e.Dst,
			Actor:       e.Actor,
			Marker:      e.Marker,
			Doc:         e.Doc,
			Yours:       e.Actor == actor,
			ToIsDerived: e.DstIsDerived,
			MovesItem:   e.Moves(),
		}
		if w.Yours {
			w.Command = commandFor(e)
		}
		out = append(out, w)
	}
	return out
}

// commandFor is what the caller runs to take an edge it owns.
//
// Empty when the edge records no marker (a silent transition has nothing to
// post) or names several (a per-stage alternation cannot be resolved without
// knowing the stage, and inventing one would hand back a command for the wrong
// stage). The required fields come from the marker package rather than being
// listed here: a marker posted without them is rejected at post time, and
// telling the caller only half the contract just moves the failure later.
func commandFor(e pipelinefsm.Event) string {
	markers := e.Markers()
	if len(markers) != 1 {
		return ""
	}
	markerType := pipelinefsm.MarkerType(markers[0])
	cmd := "human marker post <KEY> " + markerType
	for _, f := range marker.RequiredFields(markerType) {
		cmd += " --field " + f + "=<" + f + ">"
	}
	return cmd
}

func buildMarkerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "marker NAME",
		Short: "What a marker records, and where posting it moves an item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := load()
			if err != nil {
				return err
			}
			name := pipelinefsm.MarkerType(args[0])
			uses := doc.MarkerUses(name)
			listed, dualRole := doc.RecordsContentOnly(name)

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
			if len(uses) == 0 && !listed {
				return errors.WithDetails("the state machine does not mention this marker",
					"marker", name, "hint", "human fsm states lists the machine; human marker post accepts open-ended types the machine does not model")
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
