package daemon

import (
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
	if deps.Progress == nil || card.Stage == "" {
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
	if deps.Attempts == nil || card.Stage == "" {
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
