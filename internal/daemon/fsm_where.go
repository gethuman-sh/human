package daemon

import (
	"sort"
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/pipelinefsm"
	"github.com/gethuman-sh/human/internal/tracker"
)

// WhereReport answers "where is this item, and what may I do about it" for one
// ticket. It is the answer an agent can actually ask for, because the one thing
// an agent always knows is its own key.
type WhereReport struct {
	Key             string `json:"key"`
	DocumentVersion int    `json:"document_version"`

	// Board is where the derivation puts the card. It is the ground truth here:
	// the derivation never refuses, so this is always answerable even when the
	// machine has no name for the result.
	Board WhereBoard `json:"board"`

	// State is the single state the item is in, when the evidence identifies
	// one. Empty when it does not, in which case Candidates carries what it
	// could be and Why says what stopped it narrowing further. Reporting
	// nothing is better than reporting the wrong one: an agent that acts on a
	// state it is not in takes an edge it does not own.
	State      string       `json:"state,omitempty"`
	Candidates []WhereState `json:"candidates,omitempty"`
	Why        string       `json:"why,omitempty"`

	// Entered is when the item arrived where it is, and what recorded that.
	//
	// Without it stale_when cannot be evaluated by the thing reading it: the rule
	// says "past StuckRunningGrace with no live agent", and an asker that knows
	// only its agent's idle time cannot tell a card parked for a minute from one
	// parked for three days. The agent's liveness and the item's age are
	// different clocks and only one of them was being reported.
	Entered *WhereEntered `json:"entered,omitempty"`

	// History is where the item has BEEN, oldest first, ending before where it is
	// now. In states rather than markers, because the asker already thinks in
	// states — that is the vocabulary the rest of this answer uses, and making it
	// translate marker names into positions is how it gets one wrong.
	//
	// It deliberately stops short of the current position, which lives in State
	// and Entered. A trail whose last entry might or might not be "now" is the
	// confusing shape.
	History []WhereEvent `json:"history,omitempty"`

	// Agent is the liveness of the stage's agent, read from the daemon's own
	// records rather than recomputed. A second implementation of "is it alive"
	// is the exact defect class the machine document exists to prevent.
	Agent *WhereAgent `json:"agent,omitempty"`

	// Budget is what the item has already spent of its automatic relaunches.
	Budget *WhereBudget `json:"budget,omitempty"`
}

// WhereBoard is the derived placement.
type WhereBoard struct {
	Stage string `json:"stage"`
	State string `json:"state"`
}

// WhereState is one state the item may be in, with everything needed to decide
// what to do about it.
type WhereState struct {
	Name             string               `json:"name"`
	Doc              string               `json:"doc,omitempty"`
	Holds            string               `json:"holds,omitempty"`
	WhoMayAct        []string             `json:"who_may_act,omitempty"`
	StaleWhen        string               `json:"stale_when,omitempty"`
	IfNothingHappens string               `json:"if_nothing_happens,omitempty"`
	WaysOut          []pipelinefsm.WayOut `json:"ways_out"`
}

// WhereAgent reports the stage agent's liveness.
type WhereAgent struct {
	Name string `json:"name"`
	// Known is false when the daemon has no record — it restarted, or the agent
	// has yet to emit anything. Reported rather than folded into "not alive",
	// because absent evidence and evidence of absence lead to opposite actions.
	Known           bool   `json:"known"`
	LastEventAt     string `json:"last_event_at,omitempty"`
	IdleSeconds     int    `json:"idle_seconds,omitempty"`
	Stalled         bool   `json:"stalled"`
	InsideTool      bool   `json:"inside_tool,omitempty"`
	OutstandingCall bool   `json:"outstanding_model_request,omitempty"`
	Blocked         bool   `json:"blocked,omitempty"`
}

// WhereEntered is when the item arrived where it is.
type WhereEntered struct {
	At      string `json:"at"`
	Seconds int    `json:"seconds"`
	Via     string `json:"via"`
}

// WhereEvent is one position the item held, and when it got there.
type WhereEvent struct {
	// State is where the marker took it, when the machine names exactly one
	// destination for that marker. Empty when it names several or none — an
	// honest blank beats a plausible guess in a trail an agent reasons from.
	State string `json:"state,omitempty"`
	// Marker is always present, so an entry whose state could not be named is
	// still legible.
	Marker     string `json:"marker"`
	At         string `json:"at"`
	SecondsAgo int    `json:"seconds_ago"`
}

// DefaultWhereHistory is how many past markers ride along by default.
//
// Ten covers a full PR review→fix loop (DefaultPRReviewRounds is 3, two markers
// a round) plus what preceded it, which is the question history actually
// answers: not "what happened" — the ticket has that — but "is this my first
// pass or my third", because a card round the loop twice needs a different move
// from one on its first attempt, and the retry budget counts stage relaunches
// rather than loop rounds.
const DefaultWhereHistory = 10

// WhereBudget is the automatic-relaunch budget for the item's current stage.
type WhereBudget struct {
	Kind  string `json:"kind"`
	Spent int    `json:"spent"`
	Of    int    `json:"of"`
}

// WhereDeps are the daemon-held facts a report needs beyond the ticket's own
// comments. Each is optional: a missing one drops its section rather than
// failing the answer, because "where am I" is still worth answering when only
// the liveness record is unavailable.
type WhereDeps struct {
	// Progress reports the last sign of life from an agent, nil-safe.
	Progress AgentProgressProbe
	// Attempts reads how many automatic relaunches a stage has already spent.
	// It must READ ONLY — StageRetry.Attempts increments, and a question must
	// never spend the budget it is asking about.
	Attempts func(pmKey string, stage BoardStage) (int, error)
	// MaxAttempts is the cap those attempts are counted against.
	MaxAttempts int
	// HistoryLimit bounds the trail. Zero means DefaultWhereHistory; negative
	// means no history at all, for a caller that only wants the position.
	HistoryLimit int
	// Now is injected so the staleness answer is testable.
	Now time.Time
}

// BuildWhere resolves one ticket's position. Pure given its inputs.
func BuildWhere(doc pipelinefsm.Document, key string, comments []tracker.Comment, status tracker.Category, isIdea bool, actor string, deps WhereDeps) WhereReport {
	card := DeriveBoardCard(comments, status, isIdea)
	report := WhereReport{
		Key:             key,
		DocumentVersion: doc.Version,
		Board:           WhereBoard{string(card.Stage), string(card.State)},
	}

	states, why := statesFor(doc, card, comments)
	report.Why = why
	for _, s := range states {
		report.Candidates = append(report.Candidates, WhereState{
			Name:             s.Name,
			Doc:              s.Doc,
			Holds:            s.Holds,
			WhoMayAct:        s.WhoMayAct,
			StaleWhen:        s.StaleWhen,
			IfNothingHappens: s.IfNothingHappens,
			WaysOut:          doc.WaysOut(s.Name, actor),
		})
	}
	if len(states) == 1 {
		report.State = states[0].Name
	}

	report.Entered = whereEntered(doc, comments, deps.Now)
	report.History = whereHistory(doc, comments, deps.HistoryLimit, deps.Now)
	report.Agent = whereAgent(key, card, deps)
	report.Budget = whereBudget(key, card, deps)
	return report
}

// statesFor names the state an item is in, or every state it could be in.
//
// The board placement decides it outright for sixteen of the eighteen
// placements the machine declares. Two are genuinely many-to-one —
// implementation/running covers seven states and done/running five — because
// the board shows that a stage is running while the machine distinguishes the
// phases inside it. Those are narrowed by the newest marker in the winning
// stage, which is the record of which phase last reported.
func statesFor(doc pipelinefsm.Document, card BoardCard, comments []tracker.Comment) ([]pipelinefsm.State, string) {
	// An open decision is identified by the block, not by the placement. The
	// document says so itself — "decision needed" is not a board state, and
	// pauseOnOpenOptions turns running or failed into idle — so an item parked on
	// a fork is indistinguishable by placement from one that is merely idle.
	// Checked first because that idleness is what it shares with `filed`.
	if _, open := openOptionsBlock(comments); open {
		if s, ok := doc.FindState("awaiting-decision"); ok {
			return []pipelinefsm.State{s}, ""
		}
	}

	// A within-stage state that any column can carry is matched on the state
	// alone: an item does not leave its column to be stopped, queued or waiting
	// on the substrate, so its stage says nothing about which of those it is in.
	//
	// Only for a state that SAYS something. The empty state is idle, which every
	// column has and which distinguishes nothing — matching on it would answer
	// "awaiting-decision" for every quiet card, including a closed one.
	if card.State != "" {
		if anyStage := doc.StatesAtAnyStage(string(card.State)); len(anyStage) > 0 {
			return anyStage, ""
		}
	}

	// A closed ticket has left the pipeline. That is not a gap in the machine:
	// the machine describes how an item moves THROUGH the pipeline, and closing
	// takes it out rather than moving it within. The one real effect of closing —
	// the ticket's agents are stopped — belongs to the reaper, which follows a
	// container where this document follows a ticket, and docs/reaper.md already
	// owns it. Saying "gap in the document" here sent a reader to fix the wrong
	// file.
	if card.Stage == BoardHidden {
		return nil, "this ticket is closed, so it is no longer in the pipeline and no state describes it — " +
			"closing takes an item out rather than moving it, and stopping its agents is the reaper's job (docs/reaper.md)"
	}

	candidates := doc.StatesAtPlacement(string(card.Stage), string(card.State))
	switch len(candidates) {
	case 0:
		return nil, "no state in the machine describes the board placement " +
			string(card.Stage) + "/" + string(card.State) +
			" — the item is somewhere the machine cannot name, which is a gap in the document rather than in the item"
	case 1:
		return candidates, ""
	}

	// Narrow by the marker that last reported inside the winning stage: it names
	// the phase that recorded, where the placement only names the stage.
	_, latest := latestStateInStage(comments, card.Stage)
	header := markerHeaderOf(latest.Body)
	if header == "" {
		return candidates, "the board says the " + string(card.Stage) +
			" stage is running; nothing narrows which phase of it, so every phase is listed"
	}
	narrowed := intersectByName(candidates, doc.StatesEnteredBy(header))
	if len(narrowed) == 0 {
		return candidates, "the newest " + string(card.Stage) + " marker (" + header +
			") does not lead to any phase the placement allows, so every phase is listed"
	}
	if len(narrowed) == 1 {
		return narrowed, ""
	}
	return narrowed, "the newest " + string(card.Stage) + " marker (" + header +
		") is posted by more than one phase, so the ones it could be are listed"
}

// classifiedMarkers returns the pipeline markers in the thread, oldest first,
// paired with the times their comments were posted.
//
// The times come from the comments themselves — a marker IS a comment, so when
// it was posted is when the item moved. Nothing else in the tool records that:
// `human marker list` reports type and fields only, so before this an asker
// could see what had happened and never when.
func classifiedMarkers(comments []tracker.Comment) []tracker.Comment {
	var out []tracker.Comment
	for _, c := range comments {
		if _, _, ok := ClassifyMarker(c.Body); ok {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// whereEntered reports when the item reached where it is, from the newest
// classified marker: the one that put it there.
func whereEntered(doc pipelinefsm.Document, comments []tracker.Comment, now time.Time) *WhereEntered {
	trail := classifiedMarkers(comments)
	if len(trail) == 0 {
		return nil
	}
	latest := trail[len(trail)-1]
	return &WhereEntered{
		At:      latest.Created.UTC().Format(time.RFC3339),
		Seconds: int(now.Sub(latest.Created).Seconds()),
		Via:     markerHeaderOf(latest.Body),
	}
}

// whereHistory is where the item has BEEN, oldest first, stopping before where
// it is now — the newest marker is the current position and is reported by
// State and Entered instead.
//
// Bounded, and carrying no comment bodies. The bound is token cost; the missing
// bodies are the real rule. A full thread invites an asker to re-derive its own
// view of where the item is, which is a second implementation of the derivation
// — the defect class this machine's document exists to prevent. `human marker
// show` has the bodies for the rare case that genuinely needs one.
func whereHistory(doc pipelinefsm.Document, comments []tracker.Comment, limit int, now time.Time) []WhereEvent {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		limit = DefaultWhereHistory
	}
	trail := classifiedMarkers(comments)
	if len(trail) <= 1 {
		return nil
	}
	past := trail[:len(trail)-1]
	if limit > 0 && len(past) > limit {
		past = past[len(past)-limit:]
	}
	out := make([]WhereEvent, 0, len(past))
	for _, c := range past {
		header := markerHeaderOf(c.Body)
		event := WhereEvent{
			Marker:     header,
			At:         c.Created.UTC().Format(time.RFC3339),
			SecondsAgo: int(now.Sub(c.Created).Seconds()),
		}
		if entered := doc.StatesEnteredBy(header); len(entered) == 1 {
			event.State = entered[0].Name
		}
		out = append(out, event)
	}
	return out
}

// markerHeaderOf extracts the [human:…] header from a comment body.
func markerHeaderOf(body string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[human:") {
		return ""
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return ""
	}
	return line[:end+1]
}

// intersectByName keeps the candidates the narrowing evidence also allows.
func intersectByName(candidates, allowed []pipelinefsm.State) []pipelinefsm.State {
	permitted := make(map[string]bool, len(allowed))
	for _, s := range allowed {
		permitted[s.Name] = true
	}
	var out []pipelinefsm.State
	for _, s := range candidates {
		if permitted[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// whereAgent reads liveness from the daemon's own progress record. It never
// recomputes staleness: AgentProgress.Stalled already holds the rule, including
// that a blocked agent is waiting for a person rather than hung.
func whereAgent(key string, card BoardCard, deps WhereDeps) *WhereAgent {
	// Hidden is the board's way of saying "not on the board", not a stage work
	// runs in, so there is no agent to be live or dead. Composing a name from it
	// produced `board-SC-1234-hidden` — an agent that has never existed,
	// reported as merely unknown, which reads as "we lost track of it".
	if deps.Progress == nil || card.Stage == "" || card.Stage == BoardHidden {
		return nil
	}
	name := agentNameFor(key, card.Stage)
	p, ok := deps.Progress(name)
	if !ok {
		return &WhereAgent{Name: name, Known: false}
	}
	stalled, idle := p.Stalled(deps.Now)
	return &WhereAgent{
		Name:            name,
		Known:           true,
		LastEventAt:     p.LastEventAt.UTC().Format(time.RFC3339),
		IdleSeconds:     int(idle.Seconds()),
		Stalled:         stalled,
		InsideTool:      p.InsideTool,
		OutstandingCall: p.OutstandingModelRequest,
		Blocked:         p.Blocked,
	}
}

// whereBudget reports what the stage has already spent. Read-only by contract:
// the caller supplies a reader, never StageRetry.Attempts, which increments.
func whereBudget(key string, card BoardCard, deps WhereDeps) *WhereBudget {
	// Same as the agent: a closed ticket has no stage whose relaunches could be
	// counted, and reading a counter for `hidden` would report a budget for work
	// that will never run.
	if deps.Attempts == nil || card.Stage == "" || card.Stage == BoardHidden {
		return nil
	}
	spent, err := deps.Attempts(key, card.Stage)
	if err != nil {
		return nil
	}
	max := deps.MaxAttempts
	if max <= 0 {
		max = DefaultStageRetries
	}
	return &WhereBudget{Kind: "stage_retries", Spent: spent, Of: max}
}
