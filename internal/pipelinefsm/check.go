package pipelinefsm

import (
	"fmt"
	"sort"
	"strings"
)

// Rule names the check that produced a finding, so a failure says which
// guarantee broke rather than only that something did.
type Rule string

const (
	RuleSchema       Rule = "schema"        // the fields a machine needs are present and non-empty
	RuleTopology     Rule = "topology"      // src/dst name declared states; names are unique
	RuleReachability Rule = "reachability"  // every state is reachable, every non-terminal has an exit
	RuleTerminal     Rule = "terminal"      // terminal and reopenable agree with the edges that exist
	RuleActor        Rule = "actor"         // every transition names a declared actor
	RuleMarkerSyntax Rule = "marker-syntax" // a marker is written the way a marker is written
	RuleDescribed    Rule = "described"     // a transition says what it means and where it lives
	RuleInvariant    Rule = "invariant"     // a state says what holds, who may act, and what happens if nobody does
	RuleDualRole     Rule = "dual-role"     // a marker that both moves an item and records content says so on purpose
)

// Severity separates a machine that does not hold together from one that holds
// together but is under-described. Both are worth reporting and only the first
// should stop a build: an unreachable state or a dangling dst makes the document
// unusable for the reasoning it exists to support, while a transition missing
// its sentence of prose is a gap to fill, not a wrong answer.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Finding is one way the document fails to be a well-formed machine.
type Finding struct {
	Rule     Rule     `json:"rule"`
	Severity Severity `json:"severity"`
	Subject  string   `json:"subject"` // the transition or state at fault
	Message  string   `json:"message"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%-7s %-14s %-28s %s", f.Severity, f.Rule, f.Subject, f.Message)
}

// Errors counts the findings severe enough to fail a build.
func Errors(findings []Finding) int {
	n := 0
	for _, f := range findings {
		if f.Severity == SeverityError {
			n++
		}
	}
	return n
}

// markerPrefix is what makes a string a marker header.
const markerPrefix = "[human:"

// Validate runs every rule and returns ALL findings, sorted.
//
// All of them on purpose. The hand-written tests this grew out of used require,
// so the first disagreement hid the rest and each fix revealed one more. A
// document that has drifted has usually drifted in several places at once — that
// is what drift is — so the useful output is the whole list.
func Validate(doc Document) []Finding {
	var findings []Finding
	findings = append(findings, checkSchema(doc)...)
	findings = append(findings, checkTopology(doc)...)
	findings = append(findings, checkReachability(doc)...)
	findings = append(findings, checkTerminals(doc)...)
	findings = append(findings, checkActors(doc)...)
	findings = append(findings, checkMarkerSyntax(doc)...)
	findings = append(findings, checkDescribed(doc)...)
	findings = append(findings, checkInvariants(doc)...)
	findings = append(findings, checkDualRole(doc)...)

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return findings[i].Severity < findings[j].Severity // error before warning
		}
		if findings[i].Rule != findings[j].Rule {
			return findings[i].Rule < findings[j].Rule
		}
		if findings[i].Subject != findings[j].Subject {
			return findings[i].Subject < findings[j].Subject
		}
		return findings[i].Message < findings[j].Message
	})
	return findings
}

// Check validates the compiled-in document.
func Check() ([]Finding, error) {
	doc, err := Load()
	if err != nil {
		return nil, err
	}
	return Validate(doc), nil
}

// checkSchema requires the fields without which the rest is not a machine.
func checkSchema(doc Document) []Finding {
	var findings []Finding
	if doc.Version <= 0 {
		findings = append(findings, Finding{RuleSchema, SeverityError, "version", "is missing — the machine must declare its schema version"})
	}
	for _, missing := range []struct{ field, value string }{
		{"describes", doc.Describes},
		{"item", doc.Item},
		{"initial", doc.Initial},
	} {
		if strings.TrimSpace(missing.value) == "" {
			findings = append(findings, Finding{RuleSchema, SeverityError, missing.field, "is empty — the machine must declare it"})
		}
	}
	if len(doc.States) == 0 {
		findings = append(findings, Finding{RuleSchema, SeverityError, "states", "the machine declares no states"})
	}
	if len(doc.Events) == 0 {
		findings = append(findings, Finding{RuleSchema, SeverityError, "events", "the machine declares no transitions"})
	}
	if len(doc.Actors) == 0 {
		findings = append(findings, Finding{RuleSchema, SeverityError, "actors", "the machine declares no actors"})
	}

	for i, s := range doc.States {
		if strings.TrimSpace(s.Name) == "" {
			findings = append(findings, Finding{RuleSchema, SeverityError, fmt.Sprintf("states[%d]", i), "a state has no name"})
		}
	}
	for i, e := range doc.Events {
		if strings.TrimSpace(e.Name) == "" {
			findings = append(findings, Finding{RuleSchema, SeverityError, fmt.Sprintf("events[%d]", i), "a transition has no name"})
		}
	}
	return findings
}

// checkDescribed is the completeness pass: a state that does not say what it
// means, or a transition that says neither what it means nor where it lives, is
// a row in a table rather than a description. Warnings, not errors — the machine
// still holds together, and a build that goes red over a missing sentence
// teaches people to stop adding transitions rather than to describe them.
func checkDescribed(doc Document) []Finding {
	var findings []Finding
	for _, s := range doc.States {
		if s.Name != "" && strings.TrimSpace(s.Doc) == "" {
			findings = append(findings, Finding{RuleDescribed, SeverityWarning, s.Name, "a state should say what it means"})
		}
	}
	for _, e := range doc.Events {
		if e.Name == "" {
			continue
		}
		if strings.TrimSpace(e.Doc) == "" {
			findings = append(findings, Finding{RuleDescribed, SeverityWarning, e.Name, "a transition should say what it means"})
		}
		if strings.TrimSpace(e.Implementation()) == "" {
			findings = append(findings, Finding{RuleDescribed, SeverityWarning, e.Name, "a transition should say where it is implemented (where: or prompt:)"})
		}
	}
	return findings
}

// checkInheritance guards the shared rules themselves. Two ways they rot: a
// state inheriting a default nobody declares (it then silently has no answer at
// all), and a state restating verbatim what it already inherits — which is the
// duplication the defaults were extracted to remove, growing back one paste at a
// time. A `note` is the way to record a deviation without restating the rule.
func checkInheritance(doc Document) []Finding {
	var findings []Finding
	for _, s := range doc.States {
		if s.Inherits == "" {
			if s.Note != "" {
				findings = append(findings, Finding{
					RuleInvariant, SeverityWarning, s.Name,
					"has a note but inherits nothing — the note has no rule to add to",
				})
			}
			continue
		}
		base, ok := doc.StageDefaults.Rules[s.Inherits]
		if !ok || (base.StaleWhen == "" && base.IfNothingHappens == "") {
			findings = append(findings, Finding{
				RuleInvariant, SeverityError, s.Name,
				fmt.Sprintf("inherits %q, which stage_defaults does not declare", s.Inherits),
			})
			continue
		}
		if s.StaleWhen == base.StaleWhen && s.StaleWhen != "" {
			findings = append(findings, Finding{RuleInvariant, SeverityWarning, s.Name,
				"restates the stale_when it already inherits — delete it, or say how it differs"})
		}
		if s.IfNothingHappens == base.IfNothingHappens && s.IfNothingHappens != "" {
			findings = append(findings, Finding{RuleInvariant, SeverityWarning, s.Name,
				"restates the if_nothing_happens it already inherits — delete it, or put the difference in `note`"})
		}
	}
	return findings
}

// checkTopology holds the graph to its own terms: a transition may not come from
// or lead to a state nobody declared, and no two may share a name — the name is
// the alphabet, and a duplicate silently shadows one of them.
func checkTopology(doc Document) []Finding {
	var findings []Finding
	states := map[string]bool{}
	for _, s := range doc.States {
		if states[s.Name] {
			findings = append(findings, Finding{RuleTopology, SeverityError, s.Name, "declared twice as a state"})
		}
		states[s.Name] = true
	}
	if doc.Initial != "" && !states[doc.Initial] {
		findings = append(findings, Finding{RuleTopology, SeverityError, doc.Initial, "the initial state is not declared"})
	}

	seen := map[string]bool{}
	for _, e := range doc.Events {
		if e.Name != "" && seen[e.Name] {
			findings = append(findings, Finding{RuleTopology, SeverityError, e.Name, "declared twice as a transition"})
		}
		seen[e.Name] = true

		if len(e.Src) == 0 {
			findings = append(findings, Finding{RuleTopology, SeverityError, e.Name, "a transition needs at least one source"})
		}
		if !states[e.Dst] {
			findings = append(findings, Finding{RuleTopology, SeverityError, e.Name, fmt.Sprintf("dst %q is not a declared state", e.Dst)})
		}
		fromSeen := map[string]bool{}
		for _, src := range e.Src {
			if !states[src] {
				findings = append(findings, Finding{RuleTopology, SeverityError, e.Name, fmt.Sprintf("src %q is not a declared state", src)})
			}
			if fromSeen[src] {
				findings = append(findings, Finding{RuleTopology, SeverityError, e.Name, fmt.Sprintf("src %q is listed twice", src)})
			}
			fromSeen[src] = true
		}
	}
	return findings
}

// checkReachability finds description nobody can reach and states nobody can
// leave. An unreachable state is documentation of something that cannot happen;
// a non-terminal state with no exit is a trap the machine can enter and never
// leave, which is the shape of every stuck card this document was written for.
func checkReachability(doc Document) []Finding {
	reached := map[string]bool{doc.Initial: true}
	for range doc.States { // a fixpoint: |states| rounds always suffice
		for _, e := range doc.Events {
			for _, src := range e.Src {
				if reached[src] {
					reached[e.Dst] = true
				}
			}
		}
	}

	var findings []Finding
	for _, s := range doc.States {
		if !reached[s.Name] {
			findings = append(findings, Finding{RuleReachability, SeverityError, s.Name, fmt.Sprintf("unreachable from %q", doc.Initial)})
		}
		if !s.Terminal && !hasExit(doc, s.Name) {
			findings = append(findings, Finding{RuleReachability, SeverityError, s.Name, "not terminal and has no way out"})
		}
	}
	return findings
}

// checkTerminals holds the two terminal fields to the edges that actually exist.
// A terminal state the machine can leave is a contradiction unless the document
// says a person may overrule it, and a reopenable state that is not terminal is
// a field with nothing to mean.
func checkTerminals(doc Document) []Finding {
	var findings []Finding
	for _, s := range doc.States {
		switch {
		case s.Reopenable && !s.Terminal:
			findings = append(findings, Finding{RuleTerminal, SeverityError, s.Name, "reopenable but not terminal — there is nothing to reopen"})
		case s.Terminal && movingExit(doc, s.Name) && !s.Reopenable:
			findings = append(findings, Finding{RuleTerminal, SeverityError, s.Name, "terminal but a transition moves the item out of it — mark it reopenable or stop calling it terminal"})
		case s.Terminal && s.Reopenable && !movingExit(doc, s.Name):
			findings = append(findings, Finding{RuleTerminal, SeverityError, s.Name, "reopenable but no transition leaves it — the way back is not described"})
		}
	}
	return findings
}

// hasExit reports whether any transition leads out of the state to a different
// one. A self-loop is not a way out.
func hasExit(doc Document, state string) bool {
	return leavesState(doc, state, false)
}

// movingExit is the stricter question the terminal rule asks: is there a way out
// that moves the ITEM. The two differ because an observability edge leaves every
// state, terminals included — a tracker that fails to load makes every item
// unobservable without any of them moving — and counting that as a way out of a
// terminal would make "terminal" impossible to declare truthfully.
func movingExit(doc Document, state string) bool {
	return leavesState(doc, state, true)
}

func leavesState(doc Document, state string, onlyMoving bool) bool {
	for _, e := range doc.Events {
		if e.Dst == state || (onlyMoving && !e.Moves()) {
			continue
		}
		for _, src := range e.Src {
			if src == state {
				return true
			}
		}
	}
	return false
}

// checkActors requires every transition to name an actor the document declares.
// Who causes a transition is half of what the machine says: it is the difference
// between something the pipeline does to an item and something only a person can
// do, and an actor invented in one entry is a distinction quietly lost.
func checkActors(doc Document) []Finding {
	var findings []Finding
	for _, e := range doc.Events {
		if strings.TrimSpace(e.Actor) == "" {
			findings = append(findings, Finding{RuleActor, SeverityError, e.Name, "a transition must name the actor that causes it"})
			continue
		}
		if _, ok := doc.Actors[e.Actor]; !ok {
			findings = append(findings, Finding{RuleActor, SeverityError, e.Name, fmt.Sprintf("actor %q is not declared", e.Actor)})
		}
	}
	for _, s := range doc.States {
		for _, actor := range s.WhoMayAct {
			if _, ok := doc.Actors[actor]; !ok {
				findings = append(findings, Finding{RuleActor, SeverityError, s.Name, fmt.Sprintf("who_may_act names %q, which is not a declared actor", actor)})
			}
		}
	}
	return append(findings, checkActorsAgree(doc)...)
}

// checkActorsAgree is the one rule that makes the two halves of the document
// check each other: who a state says may act on it, against who the transitions
// leaving it are actually caused by. Written separately they drift, and the
// drift is silent — a state claiming only a person can move it while a daemon
// transition leaves it is the "machine acts, never asks" rule broken in
// description before it is broken in code.
//
// Non-moving transitions are excluded. An observability edge leaves every state
// including the terminals without anything acting on the item, so counting it
// would force `daemon` into every state's list and make the rule say nothing.
func checkActorsAgree(doc Document) []Finding {
	declared := map[string]map[string]bool{}
	for _, s := range doc.States {
		if !s.HasInvariants() {
			continue // not filled in; the invariant rule reports that
		}
		allowed := map[string]bool{}
		for _, a := range s.WhoMayAct {
			allowed[a] = true
		}
		declared[s.Name] = allowed
	}

	var findings []Finding
	seen := map[string]bool{}
	for _, e := range doc.Events {
		if !e.Moves() || e.Actor == "" {
			continue
		}
		for _, src := range e.Src {
			allowed, known := declared[src]
			if !known || allowed[e.Actor] {
				continue
			}
			if key := src + "/" + e.Actor; !seen[key] {
				seen[key] = true
				findings = append(findings, Finding{
					RuleActor, SeverityError, src,
					fmt.Sprintf("who_may_act omits %q, but %q leaves this state and is caused by it", e.Actor, e.Name),
				})
			}
		}
	}
	return findings
}

// checkInvariants is the completeness pass for what a state guarantees while an
// item sits in it. Warnings for the prose, like any other description; an error
// only for the one case that contradicts itself — a state nobody may act on that
// is not actually an end.
func checkInvariants(doc Document) []Finding {
	findings := checkInheritance(doc)
	for _, s := range doc.ResolvedStates() {
		if s.Name == "" {
			continue
		}
		if !s.HasInvariants() {
			findings = append(findings, Finding{RuleInvariant, SeverityWarning, s.Name, "should say who may act on it (who_may_act)"})
		} else if len(s.WhoMayAct) == 0 && (!s.Terminal || movingExit(doc, s.Name)) {
			findings = append(findings, Finding{
				RuleInvariant, SeverityError, s.Name,
				"nobody may act on it, yet it is not an end — an item reaching it can never leave",
			})
		}
		for _, missing := range []struct{ field, value string }{
			{"holds", s.Holds},
			{"stale_when", s.StaleWhen},
			{"if_nothing_happens", s.IfNothingHappens},
		} {
			if strings.TrimSpace(missing.value) == "" {
				findings = append(findings, Finding{RuleInvariant, SeverityWarning, s.Name, "should say " + missing.field})
			}
		}
	}
	return findings
}

// checkMarkerSyntax checks the shape of a marker, not its existence — whether
// the daemon defines it is a question about the code. A marker that is not
// written as one is still a document bug: it will not match anything a reader
// greps for in a real comment thread.
func checkMarkerSyntax(doc Document) []Finding {
	var findings []Finding
	for _, e := range doc.Events {
		if strings.TrimSpace(e.Marker) == "" {
			continue // a transition need not be recorded by a marker
		}
		for _, m := range e.Markers() {
			if !strings.HasPrefix(m, markerPrefix) || !strings.HasSuffix(m, "]") || len(m) <= len(markerPrefix)+1 {
				findings = append(findings, Finding{RuleMarkerSyntax, SeverityError, e.Name, fmt.Sprintf("marker %q is not written as [human:…]", m)})
			}
		}
	}
	return findings
}

// checkDualRole holds the line between a deliberate overlap and an accidental
// one.
//
// A marker may legitimately be a transition from one state and a record of
// content from another — [human:plan] is both — and the flat marker list cannot
// express that, so the overlap has to be allowed. What must not be allowed is an
// overlap nobody meant: a marker added to a transition while it still sits in
// the unclassified list is a marker replay will silently absorb wherever no edge
// happens to exist, which hides real disagreements rather than recording them.
// Declaring it costs a sentence and turns silence into a decision.
func checkDualRole(doc Document) []Finding {
	var findings []Finding
	transitions := map[string]bool{}
	for _, e := range doc.Events {
		for _, m := range e.Markers() {
			transitions[strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(m), markerPrefix), "]")] = true
		}
	}
	unclassified := map[string]bool{}
	for _, m := range doc.Unclassified.Markers {
		unclassified[m] = true
		if transitions[m] && strings.TrimSpace(doc.Unclassified.DualRole[m]) == "" {
			findings = append(findings, Finding{RuleDualRole, SeverityError, m,
				"is declared both as a transition marker and as recording no movement, with no dual_role entry saying why — " +
					"either it is one or the other, or the overlap is deliberate and needs a reason"})
		}
	}
	for m := range doc.Unclassified.DualRole {
		if !transitions[m] || !unclassified[m] {
			findings = append(findings, Finding{RuleDualRole, SeverityWarning, m,
				"has a dual_role reason but is no longer both a transition marker and unclassified — the reason outlived what it explained"})
		}
	}
	return findings
}

// splitMarkers separates the alternatives of a marker field. Alternatives are
// written "a | b" and mean one transition recorded by a different marker per
// stage.
func splitMarkers(field string) []string {
	if strings.TrimSpace(field) == "" {
		return nil
	}
	parts := strings.Split(field, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
