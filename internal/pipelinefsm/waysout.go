package pipelinefsm

import "github.com/gethuman-sh/human/internal/marker"

// WayOut is one transition leaving a state, as the thing standing in that state
// needs to see it.
//
// Yours and Command are the load-bearing pair, and the reason this lives in the
// package rather than in a command: an asker sees EVERY way out — that is how it
// knows what waiting buys it and who it is waiting for — but only its own carry
// something it can run. An edge with no command is not an edge to improvise.
// Posting another actor's marker does not advance the item; it puts it in a
// state nobody drove it to, and everything downstream then reasons about a run
// that never happened.
type WayOut struct {
	Event   string `json:"event"`
	To      string `json:"to"`
	Actor   string `json:"actor"`
	Marker  string `json:"marker,omitempty"`
	Doc     string `json:"doc,omitempty"`
	Yours   bool   `json:"yours"`
	Command string `json:"command,omitempty"`

	// ToIsDerived says To is a placeholder: the real destination is computed
	// from the marker's body. Reported rather than hidden, so an asker does not
	// plan around a destination that was never a promise.
	ToIsDerived bool `json:"to_is_derived,omitempty"`

	// MovesItem is false for an edge that records something without moving the
	// item. An asker treating it as progress would wait for a change that is
	// never coming.
	MovesItem bool `json:"moves_item"`
}

// WaysOut lists the transitions leaving a state, in document order so the
// common path reads first, with each marked for the asker.
func (d Document) WaysOut(state, actor string) []WayOut {
	events := d.Out(state)
	out := make([]WayOut, 0, len(events))
	for _, e := range events {
		w := WayOut{
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
			w.Command = CommandFor(e, "<KEY>")
		}
		out = append(out, w)
	}
	return out
}

// CommandFor is what an asker runs to take an edge it owns, for a named item.
//
// Empty when the edge records no marker (a silent transition has nothing to
// post) or names several (a per-stage alternation cannot be resolved without
// knowing the stage, and inventing one would hand back a command for the wrong
// stage).
//
// The required fields come from the marker package rather than being listed
// here. A second list of them would be wrong the first time a field is added,
// and wrong silently: the asker would build a command that post-time validation
// then rejects, which is the failure moved later rather than prevented.
func CommandFor(e Event, key string) string {
	markers := e.Markers()
	if len(markers) != 1 {
		return ""
	}
	markerType := MarkerType(markers[0])
	cmd := "human marker post " + key + " " + markerType
	for _, f := range marker.RequiredFields(markerType) {
		cmd += " --field " + f + "=<" + f + ">"
	}
	return cmd
}
