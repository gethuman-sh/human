// Package pipelinefsm validates docs/pipeline-fsm.json — the written-down
// pipeline state machine.
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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// DocPath is the document's location relative to the repository root.
const DocPath = "docs/pipeline-fsm.json"

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

// LoadDocument reads the machine from a repository root.
func LoadDocument(root string) (Document, error) {
	path := filepath.Join(root, filepath.FromSlash(DocPath))
	raw, err := os.ReadFile(path) // #nosec G304 -- a path the operator names, in their own checkout
	if err != nil {
		return Document{}, errors.WrapWithDetails(err, "reading the pipeline machine", "path", path)
	}
	return ParseDocument(raw)
}

// ParseDocument decodes the machine. Split from LoadDocument so the rules can be
// exercised on a document held in memory.
func ParseDocument(raw []byte) (Document, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, errors.WrapWithDetails(err, "parsing the pipeline machine")
	}
	return doc, nil
}
