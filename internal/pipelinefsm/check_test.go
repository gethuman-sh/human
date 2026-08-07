package pipelinefsm_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/pipelinefsm"
)

// A minimal well-formed machine. Each test mutates one thing about it, so a
// finding can only come from the mutation — the alternative is a checker that
// passes because it never really looked.
const soundMachine = `{
  "version": 1,
  "describes": "commit abc1234",
  "item": "a ticket",
  "initial": "filed",
  "actors": {"daemon": "the host process", "user": "a person"},
  "states": [
    {"name": "filed", "doc": "the ticket exists",
     "holds": "no marker yet", "who_may_act": ["daemon"],
     "stale_when": "never", "if_nothing_happens": "nothing"},
    {"name": "working", "doc": "an agent is on it",
     "holds": "an agent owns the stage", "who_may_act": ["daemon"],
     "stale_when": "past the grace with no live agent", "if_nothing_happens": "the reconcile pass reds it"},
    {"name": "done", "doc": "shipped", "terminal": true,
     "holds": "merged", "who_may_act": [],
     "stale_when": "never", "if_nothing_happens": "nothing"}
  ],
  "events": [
    {"name": "start", "src": ["filed"], "dst": "working", "actor": "daemon",
     "marker": "[human:started]", "where": "board.go", "doc": "work begins"},
    {"name": "finish", "src": ["working"], "dst": "done", "actor": "daemon",
     "marker": "[human:deployed]", "where": "deploy.go", "doc": "work ends"}
  ]
}`

func validate(t *testing.T, machine string) []pipelinefsm.Finding {
	t.Helper()
	doc, err := pipelinefsm.ParseDocument([]byte(machine))
	require.NoError(t, err)
	return pipelinefsm.Validate(doc)
}

// mutate edits the machine as JSON so a test reads as the change it makes rather
// than as a wall of literal.
func mutate(t *testing.T, edit func(m map[string]any)) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(soundMachine), &m))
	edit(m)
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return string(out)
}

func events(t *testing.T, m map[string]any) []any {
	t.Helper()
	return m["events"].([]any)
}

func states(t *testing.T, m map[string]any) []any {
	t.Helper()
	return m["states"].([]any)
}

// requireFinding returns the finding for a (rule, subject) pair. Matching on
// both matters: one mutation can now trip several rules, and picking "the first
// of this rule" would assert on whichever happened to sort first.
func requireFinding(t *testing.T, findings []pipelinefsm.Finding, rule pipelinefsm.Rule, subject string) pipelinefsm.Finding {
	t.Helper()
	for _, f := range findings {
		if f.Rule == rule && f.Subject == subject {
			return f
		}
	}
	require.FailNow(t, "no such finding", "expected %q on %q, got %v", rule, subject, findings)
	return pipelinefsm.Finding{}
}

func TestValidate_ASoundMachineHasNothingToSay(t *testing.T) {
	assert.Empty(t, validate(t, soundMachine))
}

// The rule the whole thing exists for: a transition that leads somewhere nobody
// declared. It reads as a legal edge and is a dangling pointer.
func TestValidate_DstMustBeADeclaredState(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		events(t, m)[1].(map[string]any)["dst"] = "shipped"
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleTopology, "finish")
	assert.Equal(t, pipelinefsm.SeverityError, f.Severity)
	assert.Contains(t, f.Message, `dst "shipped" is not a declared state`)
}

func TestValidate_SrcMustBeADeclaredState(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		events(t, m)[0].(map[string]any)["src"] = []any{"drafted"}
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleTopology, "start")
	assert.Contains(t, f.Message, `src "drafted" is not a declared state`)
}

// A duplicate name silently shadows one of the two: the machine keeps working
// and one transition stops existing.
func TestValidate_TransitionNamesAreUnique(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		events(t, m)[1].(map[string]any)["name"] = "start"
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleTopology, "start")
	assert.Contains(t, f.Message, "declared twice as a transition")
}

func TestValidate_StateNamesAreUnique(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		states(t, m)[1].(map[string]any)["name"] = "filed"
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleTopology, "filed")
	assert.Contains(t, f.Message, "declared twice as a state")
}

func TestValidate_TheInitialStateMustExist(t *testing.T) {
	machine := mutate(t, func(m map[string]any) { m["initial"] = "drafted" })
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleTopology, "drafted")
	assert.Contains(t, f.Message, "the initial state is not declared")
}

func TestValidate_ASourceListedTwiceIsAMistake(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		events(t, m)[0].(map[string]any)["src"] = []any{"filed", "filed"}
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleTopology, "start")
	assert.Contains(t, f.Message, `src "filed" is listed twice`)
}

// Description of something that cannot happen.
func TestValidate_EveryStateMustBeReachable(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		m["states"] = append(states(t, m), map[string]any{"name": "orphan", "doc": "nothing reaches this", "terminal": true,
			"holds": "unreachable", "who_may_act": []any{}, "stale_when": "never", "if_nothing_happens": "nothing"})
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleReachability, "orphan")
	assert.Contains(t, f.Message, `unreachable from "filed"`)
}

// The shape of every stuck card the document was written for: a state the
// machine can enter and never leave.
func TestValidate_ANonTerminalStateNeedsAWayOut(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		states(t, m)[2].(map[string]any)["terminal"] = false
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleReachability, "done")
	assert.Contains(t, f.Message, "no way out")
}

// A self-loop is not a way out — it is the stuck state drawn as a circle.
func TestValidate_ASelfLoopIsNotAWayOut(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		states(t, m)[2].(map[string]any)["terminal"] = false
		m["events"] = append(events(t, m), map[string]any{
			"name": "retry-done", "src": []any{"done"}, "dst": "done",
			"actor": "daemon", "where": "deploy.go", "doc": "tries again",
		})
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleReachability, "done")
	assert.Contains(t, f.Message, "no way out")
}

func TestValidate_ATerminalWithAWayOutMustSayItIsReopenable(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		m["events"] = append(events(t, m), map[string]any{
			"name": "reopen", "src": []any{"done"}, "dst": "working",
			"actor": "user", "where": "board.go", "doc": "a person overrules it",
		})
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleTerminal, "done")
	assert.Contains(t, f.Message, "mark it reopenable")
}

func TestValidate_AReopenableTerminalWithAWayOutIsFine(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		states(t, m)[2].(map[string]any)["reopenable"] = true
		states(t, m)[2].(map[string]any)["who_may_act"] = []any{"user"}
		m["events"] = append(events(t, m), map[string]any{
			"name": "reopen", "src": []any{"done"}, "dst": "working",
			"actor": "user", "where": "board.go", "doc": "a person overrules it",
		})
	})
	assert.Empty(t, validate(t, machine))
}

func TestValidate_AReopenableTerminalWithNoWayBackIsAnEmptyPromise(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		states(t, m)[2].(map[string]any)["reopenable"] = true
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleTerminal, "done")
	assert.Contains(t, f.Message, "the way back is not described")
}

func TestValidate_ReopenableOnANonTerminalMeansNothing(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		states(t, m)[1].(map[string]any)["reopenable"] = true
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleTerminal, "working")
	assert.Contains(t, f.Message, "there is nothing to reopen")
}

// An edge that does not move the item leaves every state including a terminal —
// a tracker that fails to load makes every item unobservable without any of them
// moving. Counting that as a way out would make "terminal" undeclarable.
func TestValidate_ANonMovingEdgeDoesNotContradictATerminal(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		m["states"] = append(states(t, m), map[string]any{"name": "unobservable", "doc": "the source could not be read",
			"holds": "nothing is known", "who_may_act": []any{"daemon"}, "stale_when": "n/a", "if_nothing_happens": "the next fetch re-derives it"})
		m["events"] = append(events(t, m),
			map[string]any{
				"name": "source-unreadable", "src": []any{"filed", "working", "done"}, "dst": "unobservable",
				"actor": "daemon", "where": "compose.go", "doc": "the tracker failed to load", "moves_item": false,
			},
			map[string]any{
				"name": "source-readable-again", "src": []any{"unobservable"}, "dst": "filed",
				"actor": "daemon", "where": "compose.go", "doc": "the source came back", "moves_item": false,
			})
	})
	assert.Empty(t, validate(t, machine))
}

func TestValidate_EveryTransitionNamesADeclaredActor(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		events(t, m)[0].(map[string]any)["actor"] = "robot"
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleActor, "start")
	assert.Contains(t, f.Message, `actor "robot" is not declared`)
}

func TestValidate_ATransitionWithNoActorIsIncomplete(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		delete(events(t, m)[0].(map[string]any), "actor")
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleActor, "start")
	assert.Contains(t, f.Message, "must name the actor")
}

func TestValidate_AMarkerMustBeWrittenAsOne(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		events(t, m)[0].(map[string]any)["marker"] = "started"
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleMarkerSyntax, "start")
	assert.Contains(t, f.Message, "is not written as [human:")
}

// One transition may be recorded by a different marker per stage; every
// alternative is checked, not just the first.
func TestValidate_EveryMarkerAlternativeIsChecked(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		events(t, m)[0].(map[string]any)["marker"] = "[human:started] | oops"
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleMarkerSyntax, "start")
	assert.Contains(t, f.Message, `"oops"`)
}

func TestValidate_ATransitionNeedNotHaveAMarker(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		delete(events(t, m)[0].(map[string]any), "marker")
	})
	assert.Empty(t, validate(t, machine))
}

func TestValidate_MissingTopLevelFieldsAreErrors(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		delete(m, "version")
		delete(m, "describes")
		delete(m, "actors")
	})
	findings := validate(t, machine)
	subjects := map[string]bool{}
	for _, f := range findings {
		if f.Rule == pipelinefsm.RuleSchema {
			subjects[f.Subject] = true
		}
	}
	assert.True(t, subjects["version"], "a machine must declare its schema version")
	assert.True(t, subjects["describes"], "a machine must say what it describes")
	assert.True(t, subjects["actors"], "a machine must declare its actors")
}

// Prose gaps are warnings: the machine still holds together, and a build that
// goes red over a missing sentence teaches people to stop adding transitions
// rather than to describe them.
func TestValidate_AnUndescribedTransitionWarnsRatherThanFails(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		delete(events(t, m)[0].(map[string]any), "doc")
		delete(events(t, m)[0].(map[string]any), "where")
	})
	findings := validate(t, machine)
	require.NotEmpty(t, findings)
	assert.Zero(t, pipelinefsm.Errors(findings), "an under-described machine is not a broken one")
	for _, f := range findings {
		assert.Equal(t, pipelinefsm.SeverityWarning, f.Severity)
	}
}

// A transition implemented in a prompt is as real as one implemented in Go:
// "after X, post Y" defines an edge whether or not a compiler ever sees it.
func TestValidate_APromptCountsAsSayingWhereATransitionLives(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		e := events(t, m)[0].(map[string]any)
		delete(e, "where")
		e["prompt"] = "human-planner-agent.md"
	})
	assert.Empty(t, validate(t, machine))
}

func TestParseDocument_RejectsNonsense(t *testing.T) {
	_, err := pipelinefsm.ParseDocument([]byte("not json"))
	require.Error(t, err)
}

// The invariants: what holds while an item SITS in a state. The transition
// table says how an item moves; "nothing happened" has no row in it, which is
// why it kept being nobody's case.

// The rule that makes the two halves of the document check each other. Written
// separately they drift silently — a state claiming only a person can move it
// while a daemon transition leaves it is "machine acts, never asks" broken in
// description before it is broken in code.
func TestValidate_WhoMayActMustAgreeWithTheTransitionsThatLeave(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		states(t, m)[1].(map[string]any)["who_may_act"] = []any{"user"}
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleActor, "working")
	assert.Equal(t, pipelinefsm.SeverityError, f.Severity)
	assert.Contains(t, f.Message, `who_may_act omits "daemon"`)
	assert.Contains(t, f.Message, `"finish" leaves this state`)
}

// An observability edge leaves every state without anything acting on the item.
// Counting it would force the daemon into every state's list and make the rule
// say nothing at all.
func TestValidate_ANonMovingEdgeDoesNotForceItsActorIntoWhoMayAct(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		m["states"] = append(states(t, m), map[string]any{
			"name": "unobservable", "doc": "the source could not be read",
			"holds": "nothing is known", "who_may_act": []any{"daemon"},
			"stale_when": "n/a", "if_nothing_happens": "the next fetch re-derives it",
		})
		m["events"] = append(events(t, m),
			map[string]any{
				"name": "source-unreadable", "src": []any{"filed", "working", "done"}, "dst": "unobservable",
				"actor": "daemon", "where": "compose.go", "doc": "the tracker failed to load", "moves_item": false,
			},
			map[string]any{
				"name": "source-readable-again", "src": []any{"unobservable"}, "dst": "filed",
				"actor": "daemon", "where": "compose.go", "doc": "the source came back", "moves_item": false,
			})
	})
	assert.Empty(t, validate(t, machine), "a non-moving edge must not drag its actor into every state")
}

func TestValidate_WhoMayActMustNameDeclaredActors(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		states(t, m)[0].(map[string]any)["who_may_act"] = []any{"robot"}
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleActor, "filed")
	assert.Contains(t, f.Message, `who_may_act names "robot"`)
}

// A state nobody may act on that is not an end is the stuck card written down.
func TestValidate_NobodyMayActIsOnlyAllowedAtAnEnd(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		states(t, m)[1].(map[string]any)["who_may_act"] = []any{}
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleInvariant, "working")
	assert.Equal(t, pipelinefsm.SeverityError, f.Severity)
	assert.Contains(t, f.Message, "can never leave")
}

// An empty list is a true terminal and must not be confused with an unfilled
// one. The fixture's `done` carries exactly that, so a sound machine passing is
// the assertion.
func TestValidate_NobodyMayActIsFineAtATrueEnd(t *testing.T) {
	assert.Empty(t, validate(t, soundMachine))
}

func TestValidate_AMissingInvariantWarnsRatherThanFails(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		s := states(t, m)[1].(map[string]any)
		delete(s, "if_nothing_happens")
		delete(s, "stale_when")
	})
	findings := validate(t, machine)
	require.Len(t, findings, 2)
	assert.Zero(t, pipelinefsm.Errors(findings))
	for _, f := range findings {
		assert.Equal(t, pipelinefsm.RuleInvariant, f.Rule)
		assert.Equal(t, "working", f.Subject)
	}
}

// An unfilled who_may_act is a gap; an explicitly empty one is an answer. A
// slice cannot tell them apart, so the document's absence has to.
func TestValidate_AnUnfilledWhoMayActIsAGapNotATerminal(t *testing.T) {
	machine := mutate(t, func(m map[string]any) {
		delete(states(t, m)[1].(map[string]any), "who_may_act")
	})
	f := requireFinding(t, validate(t, machine), pipelinefsm.RuleInvariant, "working")
	assert.Equal(t, pipelinefsm.SeverityWarning, f.Severity)
	assert.Contains(t, f.Message, "who_may_act")
}
