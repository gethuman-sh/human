// Package pipelinefsm validates pipeline-fsm.json — the written-down pipeline
// state machine, which lives beside this code rather than under docs/ because
// it is read at runtime and checked by the build, not only by people.
//
// This checks the document against ITSELF: that it is a well-formed machine.
// Every transition comes from and leads to a state that exists, no two states or
// transitions share a name, nothing is unreachable, nothing is a trap you can
// enter and never leave, every transition names an actor the document declares
// and says where it is implemented.
//
// That is a narrower job than "is the document true", and deliberately so. A
// description can be perfectly consistent and still describe the wrong system;
// catching that needs the code, and it is a different check. What this catches
// is the failure that comes first and costs the most to debug later: a machine
// that does not hold together, where a transition points at a state nobody
// declared or a state has no way out, so the document cannot be reasoned about
// at all — the thing it exists to make possible.
package pipelinefsm

import (
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// DocPath is where the document lives, for messages that have to name it.
const DocPath = "internal/pipelinefsm/pipeline-fsm.json"

// raw is the machine, compiled in.
//
// It is the same machine for every project — human's pipeline, not the
// checkout's — so a reader should never have to say which copy it means, and
// nothing that asks should need a checkout to ask from. That is what lets an
// agent inside a container query the machine it is running in.
//
//go:embed pipeline-fsm.json
var raw []byte

// Document is the written machine.
//
// A PARTIAL view on purpose: the file carries prose annotations this package has
// no opinion about (runs, prompt, guard, state_key, …) and Go's decoder drops
// unknown fields, so the document can grow annotations without this type
// changing. What is modelled here is exactly what can be checked.
type Document struct {
	Version   int               `json:"version"`
	Describes string            `json:"describes"`
	Item      string            `json:"item"`
	Initial   string            `json:"initial"`
	Actors    map[string]string `json:"actors"`
	States    []State           `json:"states"`
	Events    []Event           `json:"events"`

	// StageDefaults holds the liveness rules states inherit.
	StageDefaults StageDefaults `json:"stage_defaults"`

	Unclassified Unclassified `json:"unclassified_markers"`
}

// ResolvedStates returns every state with its inherited fields filled in.
func (d Document) ResolvedStates() []State {
	out := make([]State, 0, len(d.States))
	for _, s := range d.States {
		out = append(out, s.Resolve(d.StageDefaults))
	}
	return out
}

// State is one state an item can be in.
type State struct {
	Name string `json:"name"`
	Doc  string `json:"doc"`

	// Terminal marks a state the machine may come to rest in, which is what
	// exempts it from needing a way out.
	Terminal bool `json:"terminal"`

	// Reopenable marks a terminal a person may overrule. It is the ONLY reason a
	// terminal state may have an outgoing transition, so the two fields have to
	// agree: a terminal with an exit and no reopenable is either mislabelled or
	// describes a way out nobody meant to offer.
	Reopenable bool `json:"reopenable"`

	// The four invariants. The transition table says how an item MOVES; these say
	// what is true while it does not — which is what the pipeline's stuck-card
	// bugs kept turning out to be. "Nothing happened" has no row in a transition
	// table, and that is exactly why it kept being nobody's case.
	Holds            string   `json:"holds"`
	WhoMayAct        []string `json:"who_may_act"`
	StaleWhen        string   `json:"stale_when"`
	IfNothingHappens string   `json:"if_nothing_happens"`

	// Inherits names a stage default to take StaleWhen and IfNothingHappens
	// from. Seven running states share one liveness rule, and seven copies of it
	// would need seven coordinated edits to stay true — the drift this document
	// exists to prevent, reproduced inside it.
	Inherits string `json:"inherits"`

	// Note is what is true of THIS state on top of the inherited rule, so a
	// deviation can be recorded without restating the rule it deviates from.
	Note string `json:"note"`

	// Board is where a reader would find an item in this state, as a placement
	// the code can be asked about rather than a sentence a person reads. It used
	// to be prose ("Fix, running"), which named frontend columns rather than the
	// stages the derivation actually produces — so the one question worth asking
	// of it could not be asked: is there a place a card can be that no state here
	// describes?
	Board BoardPlacement `json:"board"`
}

// BoardPlacement is where a state's item sits on the board: the stage column
// and the within-stage status the derivation would report for it.
//
// Two escape values, both meaning "not one placement": "any" for a state that
// can be reached from any column (an item does not leave its column to be
// stopped), and "none" for one the board does not place at all.
type BoardPlacement struct {
	Stage string `json:"stage"`
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
}

// StageDefaults carries the shared rules and the prose explaining them. The
// rules live under their own key so the map stays homogeneous — prose beside
// rules in one map means every reader has to know which keys are which.
type StageDefaults struct {
	Doc   string                  `json:"doc"`
	Rules map[string]StageDefault `json:"rules"`
}

// StageDefault is the liveness rule shared by every running state of one stage.
type StageDefault struct {
	StaleWhen        string `json:"stale_when"`
	IfNothingHappens string `json:"if_nothing_happens"`
}

// Resolve returns the state with its inherited fields filled in. Callers read
// the resolved state so inheritance is invisible at the point of use: what a
// reader gets is the whole answer, whether or not it was written here.
func (s State) Resolve(defaults StageDefaults) State {
	base, ok := defaults.Rules[s.Inherits]
	if !ok {
		return s
	}
	if s.StaleWhen == "" {
		s.StaleWhen = base.StaleWhen
	}
	if s.IfNothingHappens == "" {
		s.IfNothingHappens = base.IfNothingHappens
	}
	if s.Note != "" {
		s.IfNothingHappens += " " + s.Note
	}
	return s
}

// HasInvariants reports whether the state declares who may act. Distinguishes a
// state that names nobody — a true terminal — from one that has not been filled
// in, which an empty slice alone cannot.
func (s State) HasInvariants() bool {
	return s.WhoMayAct != nil
}

// Event is one transition. Name, Src and Dst are the looplab/fsm shape on
// purpose: the document stays loadable by that library if we ever want the
// machine executable rather than merely described.
type Event struct {
	Name   string   `json:"name"`
	Src    []string `json:"src"`
	Dst    string   `json:"dst"`
	Actor  string   `json:"actor"`
	Marker string   `json:"marker"`
	Doc    string   `json:"doc"`

	// Where and Prompt are the two ways a transition says where it is
	// implemented, and a transition needs one of them. Which one is not a
	// detail: `where` is Go, `prompt` is an agent instruction, and a transition
	// that lives in a prompt is exactly as real as one that lives in code —
	// "after X, post Y" defines an edge whether a compiler ever sees it.
	Where  string `json:"where"`
	Prompt string `json:"prompt"`

	// DstIsDerived says the true destination is computed rather than fixed, so
	// Dst is a topological placeholder.
	DstIsDerived bool `json:"dst_is_derived"`

	// MovesItem is false for a transition that changes something OTHER than where
	// the item is in the pipeline — an observability edge, a gate that reports
	// "not yet" without moving anything. Absent means true, so the ordinary case
	// needs no annotation. It is why a terminal state can carry an outgoing edge
	// without being a contradiction.
	MovesItem *bool `json:"moves_item"`
}

// Moves reports whether the transition changes the item's place in the pipeline.
func (e Event) Moves() bool {
	return e.MovesItem == nil || *e.MovesItem
}

// Implementation names where the transition lives, in code or in a prompt.
func (e Event) Implementation() string {
	if strings.TrimSpace(e.Where) != "" {
		return e.Where
	}
	return e.Prompt
}

// Markers splits a transition's marker field, which may name several
// alternatives ("[human:a] | [human:b]") when one transition is recorded by a
// different marker per stage.
func (e Event) Markers() []string {
	return splitMarkers(e.Marker)
}

// Load returns the compiled-in machine.
//
// There is no path argument on purpose. The document is not the checkout's, so
// "which one" was never a question worth being able to ask — and being able to
// ask it meant every caller had to know its own distance to a repository root,
// which is a fact about the caller and not about the machine.
func Load() (Document, error) {
	return ParseDocument(raw)
}

// ParseDocument decodes the machine. Split from Load so the rules can be
// exercised on a document held in memory.
func ParseDocument(raw []byte) (Document, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, errors.WrapWithDetails(err, "parsing the pipeline machine")
	}
	return doc, nil
}

// Unclassified names the markers the pipeline posts that record content rather
// than movement. Modelled so a reader replaying a real comment thread can tell
// "this marker moves nothing" from "this marker is missing from the machine".
type Unclassified struct {
	Markers []string `json:"markers"`

	// DualRole names the markers that are deliberately BOTH — a transition from
	// some states and a record of content from others — and says why for each.
	//
	// The overlap looks like a contradiction and is not one. [human:plan] moves
	// a self-planning fix run from fix-planning to fixing, and records content
	// when a planner attaches a plan to a ticket that is not going anywhere yet.
	// The list of markers has no way to say "here but not there", so without this
	// field the choice is between a document that contradicts itself and one that
	// is wrong. Declaring the overlap makes it visible and, more to the point,
	// makes an UNdeclared overlap an error rather than an accident.
	DualRole map[string]string `json:"dual_role,omitempty"`
}
