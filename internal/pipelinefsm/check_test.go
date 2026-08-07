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
    {"name": "filed", "doc": "the ticket exists"},
    {"name": "working", "doc": "an agent is on it"},
    {"name": "done", "doc": "shipped", "terminal": true}
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

// findingFor returns the first finding of a rule, so a test asserts on the rule
// it is about and not on the order findings happen to sort in.
func findingFor(findings []pipelinefsm.Finding, rule pipelinefsm.Rule) (pipelinefsm.Finding, bool) {
	for _, f := range findings {
		if f.Rule == rule {
			return f, true
		}
	}
	return pipelinefsm.Finding{}, false
}

func requireFinding(t *testing.T, findings []pipelinefsm.Finding, rule pipelinefsm.Rule, subject string) pipelinefsm.Finding {
	t.Helper()
	f, ok := findingFor(findings, rule)
	require.True(t, ok, "expected a %q finding, got %v", rule, findings)
	assert.Equal(t, subject, f.Subject)
	return f
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
		m["states"] = append(states(t, m), map[string]any{"name": "orphan", "doc": "nothing reaches this", "terminal": true})
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
		m["states"] = append(states(t, m), map[string]any{"name": "unobservable", "doc": "the source could not be read"})
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
