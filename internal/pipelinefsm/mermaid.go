package pipelinefsm

import (
	"fmt"
	"strings"
)

// Mermaid draws the machine as a state diagram.
//
// Drawing it was half the reason for writing the machine down: a list of
// transitions is checkable but not readable, and the questions people actually
// ask of a pipeline — where can this card go from here, what leads to a stuck
// state, is there a way out — are shape questions. Mermaid because it renders in
// the places this gets read: a pull request, an artifact, the docs.
func Mermaid(doc Document) string {
	var b strings.Builder
	b.WriteString("stateDiagram-v2\n")
	fmt.Fprintf(&b, "    [*] --> %s\n", id(doc.Initial))

	for _, e := range doc.Events {
		for _, src := range e.Src {
			fmt.Fprintf(&b, "    %s --> %s: %s\n", id(src), id(e.Dst), label(e))
		}
	}
	for _, s := range doc.States {
		if s.Terminal {
			fmt.Fprintf(&b, "    %s --> [*]\n", id(s.Name))
		}
	}
	return b.String()
}

// id makes a state name safe as a mermaid identifier. The names carry hyphens,
// which mermaid reads as part of an arrow.
func id(state string) string {
	return strings.ReplaceAll(state, "-", "_")
}

// label names the transition and, where one exists, the marker that records it —
// the marker is the thing a reader greps for when they are looking at a real
// ticket's comment thread and asking which edge it took.
func label(e Event) string {
	markers := e.Markers()
	if len(markers) == 0 {
		return e.Name
	}
	if len(markers) > 1 {
		return e.Name + " (per-stage marker)"
	}
	return e.Name + " " + markers[0]
}
