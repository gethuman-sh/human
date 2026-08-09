package pipelinefsm

import (
	"sort"
	"strings"
)

// The questions something INSIDE the pipeline asks: where am I, where can I go,
// what does this marker do. They live here rather than in the command that
// prints them because the answers are document logic and there will be more
// than one asker — a command today, a per-ticket lookup later — and two readings
// of the same document is the drift this package exists to prevent.

// FindState returns the named state with its inherited liveness rule filled in.
// Resolved rather than raw: a caller asking what happens if nothing happens must
// get the rule that applies, not an empty field and a pointer to a stage default
// it would then have to look up and apply itself.
func (d Document) FindState(name string) (State, bool) {
	for _, s := range d.States {
		if s.Name == name {
			return s.Resolve(d.StageDefaults), true
		}
	}
	return State{}, false
}

// StateNames lists every declared state, sorted, for an error that has to say
// what the caller could have asked for instead.
func (d Document) StateNames() []string {
	out := make([]string, 0, len(d.States))
	for _, s := range d.States {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// StatesAtPlacement returns every state the document says sits at a board
// placement, in document order.
//
// Usually one. Two placements are genuinely many-to-one — implementation/running
// covers seven states and done/running five — because the board shows a stage
// running and the machine distinguishes the phases inside it. A caller resolving
// a real item has to narrow those by other evidence, or say it could not.
//
// A state whose stage is "any" is not returned: it is reachable from every
// column, so it identifies nothing about which column an item is in. Callers
// match those on the within-stage state instead (queued, failed, outage).
func (d Document) StatesAtPlacement(stage, state string) []State {
	var out []State
	for _, s := range d.States {
		if s.Board.Stage == stage && s.Board.State == state {
			out = append(out, s.Resolve(d.StageDefaults))
		}
	}
	return out
}

// StatesAtAnyStage returns the states that sit at a within-stage state
// regardless of column — stopped, queued, substrate-down, awaiting-decision.
func (d Document) StatesAtAnyStage(state string) []State {
	var out []State
	for _, s := range d.States {
		if s.Board.Stage == "any" && s.Board.State == state {
			out = append(out, s.Resolve(d.StageDefaults))
		}
	}
	return out
}

// StatesEnteredBy returns the states a marker leads INTO — the destinations of
// every transition that records it. Used to narrow a blurred placement: the
// board says which stage is running, and the newest marker in that stage says
// which phase of it recorded that.
//
// A derived destination is skipped: it is a topological placeholder, so naming
// it would answer with a state the item is not in.
func (d Document) StatesEnteredBy(marker string) []State {
	seen := map[string]bool{}
	var out []State
	for _, e := range d.MarkerUses(marker) {
		if e.DstIsDerived || seen[e.Dst] {
			continue
		}
		if s, ok := d.FindState(e.Dst); ok {
			seen[e.Dst] = true
			out = append(out, s)
		}
	}
	return out
}

// Out returns the transitions that leave a state — the ways out, in document
// order so the common path reads first.
func (d Document) Out(state string) []Event {
	var out []Event
	for _, e := range d.Events {
		for _, src := range e.Src {
			if src == state {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// MarkerType strips the [human:…] wrapper, and leaves a bare type alone.
//
// Three forms of the same name are in play: a caller says it the way it posts it
// (`plan-ready`), the transition table writes it the way it appears on a ticket
// (`[human:plan-ready]`), and the unclassified list uses the bare form. One of
// them has to convert. Doing it here means no caller has to know which form a
// given field happens to use, which is the kind of detail that otherwise gets
// half-remembered at three call sites.
func MarkerType(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[human:")
	return strings.TrimSuffix(s, "]")
}

// MarkerHeader is the inverse: the form a marker takes on a ticket.
func MarkerHeader(markerType string) string {
	return "[human:" + MarkerType(markerType) + "]"
}

// MarkerUses returns the transitions that record a marker, named in either
// form. The document writes one transition's per-stage alternatives as
// "[human:a] | [human:b]", so a lookup has to match a member rather than the
// whole field.
func (d Document) MarkerUses(marker string) []Event {
	want := MarkerType(marker)
	var out []Event
	for _, e := range d.Events {
		for _, m := range e.Markers() {
			if MarkerType(m) == want {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// RecordsContentOnly reports whether the document declares a marker as one that
// deliberately does not move an item, and why if it says.
//
// The second return is the dual-role note, present only for a marker that is
// BOTH — a transition from some states and a record from others. A caller that
// treats "listed here" as "never a transition" would be wrong about those, which
// is exactly the confusion the dual_role block was added to make visible.
func (d Document) RecordsContentOnly(marker string) (listed bool, dualRole string) {
	want := MarkerType(marker)
	for _, m := range d.Unclassified.Markers {
		if MarkerType(m) == want {
			return true, d.Unclassified.DualRole[want]
		}
	}
	return false, ""
}
