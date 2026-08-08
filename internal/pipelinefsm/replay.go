package pipelinefsm

import (
	"encoding/json"
	"os"
	"sort"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// Replay is what a ticket's marker history says about where its item got to.
//
// It exists to ask the one question the checker cannot: not "does the document
// hold together" but "does it describe what actually happens". A marker thread
// is the pipeline's own record of every move it made, so feeding a real one
// through the written machine compares the description against the system, on
// evidence the system produced itself.
type Replay struct {
	// State is where the history ends. When Blur has more than one name the
	// history cannot say which of them it is, and State is only the first.
	State string

	// Blur is every state the item could be in without the ticket showing a
	// difference — the states reachable from State through moves that record
	// nothing. A single name means the history pinned it exactly.
	Blur []string

	// Refused is the finding. A marker the document accepts nowhere reachable
	// from where the item had got to means the pipeline made a move the machine
	// does not describe. The FIRST one is the real defect; everything after it
	// is a consequence of replay having lost the thread.
	Refused []Disagreement

	// Ambiguous is the weaker finding: the marker fits, but it fits several
	// edges leading to different states, so the header alone does not say which
	// move was made.
	Ambiguous []Disagreement

	Terminal bool
}

// Disagreement is one marker the machine could not account for, and where the
// item had got to when it arrived.
type Disagreement struct {
	Marker  string   `json:"marker"`
	State   string   `json:"state"`
	Options []string `json:"options,omitempty"` // ambiguity only: what it could have meant
}

// FirstRefusal returns the defect a history actually demonstrates, or "" for a
// clean replay. Count these, never Refused: once replay has lost the thread every
// later marker is refused too, so the raw total measures how long the history is
// rather than how wrong the document is.
func (r Replay) FirstRefusal() (Disagreement, bool) {
	if len(r.Refused) == 0 {
		return Disagreement{}, false
	}
	return r.Refused[0], true
}

// Replay feeds a ticket's markers, oldest first, through the machine. Markers may
// be written bare ("planning-started") or as they appear on a ticket
// ("[human:planning-started]").
func (d Document) Replay(markers []string) Replay {
	idx := d.newReplayIndex()
	state := d.Initial
	out := Replay{}

	for _, raw := range markers {
		m := normalizeMarker(raw)
		dsts := idx.destinations(state, m)
		switch {
		case len(dsts) == 1:
			state = dsts[0]
		case len(dsts) > 1:
			// Keep going from the first candidate rather than stopping. One
			// ambiguity early would otherwise hide every later disagreement, and
			// the whole point is to see all of them in one pass.
			out.Ambiguous = append(out.Ambiguous, Disagreement{Marker: m, State: state, Options: dsts})
			state = dsts[0]
		case idx.unclassified[m]:
			// Records content, moves nothing. Not a disagreement — the document
			// says so explicitly.
		default:
			out.Refused = append(out.Refused, Disagreement{Marker: m, State: state})
		}
	}

	out.State = state
	out.Blur = idx.closure(state)
	out.Terminal = idx.terminal[state]
	return out
}

// replayIndex is the document arranged for the question replay asks of it.
type replayIndex struct {
	// silent maps a state to what it can become without recording anything. These
	// are the moves a ticket cannot show: an agent walking from preflight to
	// triaging leaves the comment thread unchanged, so a reader replaying it must
	// treat both as possible.
	silent map[string][]string

	// moves maps a state and a marker to where that marker leads.
	moves map[string]map[string][]string

	unclassified map[string]bool
	terminal     map[string]bool
}

func (d Document) newReplayIndex() *replayIndex {
	idx := &replayIndex{
		silent:       map[string][]string{},
		moves:        map[string]map[string][]string{},
		unclassified: map[string]bool{},
		terminal:     map[string]bool{},
	}
	for _, m := range d.Unclassified.Markers {
		idx.unclassified[normalizeMarker(m)] = true
	}
	for _, s := range d.States {
		idx.terminal[s.Name] = s.Terminal
	}
	for _, e := range d.Events {
		if !e.Moves() {
			continue // an observability edge changes what we can see, not where the item is
		}
		for _, src := range e.Src {
			if strings.TrimSpace(e.Marker) == "" {
				idx.silent[src] = append(idx.silent[src], e.Dst)
				continue
			}
			if idx.moves[src] == nil {
				idx.moves[src] = map[string][]string{}
			}
			for _, m := range e.Markers() {
				idx.moves[src][normalizeMarker(m)] = append(idx.moves[src][normalizeMarker(m)], e.Dst)
			}
		}
	}
	return idx
}

// destinations returns every state the marker could have moved the item to,
// searching from everywhere it might silently already be.
func (idx *replayIndex) destinations(state, marker string) []string {
	found := map[string]bool{}
	for _, from := range idx.closure(state) {
		for _, dst := range idx.moves[from][marker] {
			found[dst] = true
		}
	}
	return sortedKeys(found)
}

// closure returns every state reachable from start without leaving a trace on the
// ticket, start included.
func (idx *replayIndex) closure(start string) []string {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range idx.silent[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return sortedKeys(seen)
}

// normalizeMarker accepts a marker in either of the two forms it is written in:
// bare in a marker listing, wrapped in the document and on a real ticket.
func normalizeMarker(m string) string {
	m = strings.TrimSpace(m)
	m = strings.TrimPrefix(m, markerPrefix)
	return strings.TrimSuffix(m, "]")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Trace is one ticket's marker history, oldest first — the unit of the corpus.
type Trace struct {
	Key     string   `json:"key"`
	Markers []string `json:"markers"`
}

// LoadCorpus reads a set of recorded histories. Only the marker TYPES are kept,
// never the bodies: the corpus has to be readable by anyone who reads the repo,
// and a marker type is vocabulary while a marker body is content.
func LoadCorpus(path string) ([]Trace, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a fixture path the caller names
	if err != nil {
		return nil, errors.WrapWithDetails(err, "reading the replay corpus", "path", path)
	}
	var traces []Trace
	if err := json.Unmarshal(raw, &traces); err != nil {
		return nil, errors.WrapWithDetails(err, "parsing the replay corpus", "path", path)
	}
	return traces, nil
}
