package pipelinefsm_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/pipelinefsm"
)

// The real machine, checked on every `make check`. This is the point of the
// package: a transition added with a dst nobody declared, or a state with no way
// out, fails the build rather than waiting to mislead whoever plans the next
// pipeline change against it.
func TestTheShippedMachineHoldsTogether(t *testing.T) {
	findings, err := pipelinefsm.Check()
	require.NoError(t, err)

	var errs []string
	for _, f := range findings {
		if f.Severity == pipelinefsm.SeverityError {
			errs = append(errs, f.String())
		}
	}
	assert.Empty(t, errs, "internal/pipelinefsm/pipeline-fsm.json is not a well-formed machine:\n%s", strings.Join(errs, "\n"))
}

func TestMermaid_DrawsEveryEdgeAndBothEnds(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	diagram := pipelinefsm.Mermaid(doc)
	assert.True(t, strings.HasPrefix(diagram, "stateDiagram-v2\n"))
	assert.Contains(t, diagram, "[*] --> "+strings.ReplaceAll(doc.Initial, "-", "_"))

	edges := 0
	for _, e := range doc.Events {
		edges += len(e.Src)
	}
	terminals := 0
	for _, s := range doc.States {
		if s.Terminal {
			terminals++
		}
	}
	// One line per (src, transition) pair, one per terminal, one for the entry,
	// one for the header.
	assert.Equal(t, edges+terminals+2, len(strings.Split(strings.TrimRight(diagram, "\n"), "\n")))

	// Hyphens are mermaid's arrow, so a state name carrying one must not reach
	// the diagram raw.
	for _, line := range strings.Split(diagram, "\n") {
		before, _, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		assert.NotContains(t, strings.TrimSpace(strings.ReplaceAll(before, "-->", "")), "-",
			"a state name reached the diagram unescaped: %s", line)
	}
}

func TestMermaid_LabelsAnEdgeWithItsMarker(t *testing.T) {
	doc, err := pipelinefsm.ParseDocument([]byte(soundMachine))
	require.NoError(t, err)
	assert.Contains(t, pipelinefsm.Mermaid(doc), "start [human:started]")
}

// A transition recorded by a different marker per stage cannot name one of them
// in the label without being wrong about the others.
func TestMermaid_SaysWhenTheMarkerIsPerStage(t *testing.T) {
	doc := pipelinefsm.Document{
		Initial: "a",
		States:  []pipelinefsm.State{{Name: "a"}, {Name: "b", Terminal: true}},
		Events: []pipelinefsm.Event{{
			Name: "down", Src: []string{"a"}, Dst: "b",
			Marker: "[human:planning-outage] | [human:deploy-outage]",
		}},
	}
	assert.Contains(t, pipelinefsm.Mermaid(doc), "down (per-stage marker)")
}

// SC-4244: a launch the single-flight guard refused records nothing and moves
// nothing, so each of its six events must be a true self-loop with no marker.
// A placeholder dst would draw a real exit in the diagram and let a future trap
// state hide behind an edge no item ever takes.
func TestTheRefusalEventsAreSelfLoopsThatRecordNothing(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	want := map[string]bool{
		"stage-relaunch-refused":      false,
		"outage-relaunch-refused":     false,
		"queued-launch-refused":       false,
		"pr-review-launch-refused":    false,
		"pr-fix-launch-refused":       false,
		"deploy-fixer-launch-refused": false,
	}
	for _, e := range doc.Events {
		if _, ok := want[e.Name]; !ok {
			continue
		}
		want[e.Name] = true
		require.Len(t, e.Src, 1, "%s: one source state, so the self-loop is unambiguous", e.Name)
		assert.Equal(t, e.Src[0], e.Dst, "%s: a refusal moves nothing", e.Name)
		assert.False(t, e.Moves(), "%s must declare moves_item: false", e.Name)
		assert.Empty(t, e.Marker, "%s: the absence of a marker IS the fix", e.Name)
		assert.NotEmpty(t, e.Doc, "%s: say why it records nothing", e.Name)
		assert.NotEmpty(t, e.Where, "%s: name the code that refuses", e.Name)
	}
	for name, found := range want {
		assert.True(t, found, "%s is missing from the document", name)
	}
}

// SC-5878: deploy-launch-deferred (the CLI route's silent wait, SC-5691) is no
// longer a true self-loop: since `human deploy` can enter the checkout wait
// from any of start-deploy's own sources — a card fresh out of planning as
// easily as a reviewed one — its src is that same wide list, while its dst
// stays the fixed placeholder "reviewed" (the ordinary case, and the one every
// existing assertion about this event already pinned). It still records
// nothing and moves nothing; only the "one source, dst equals it" shape the
// six single-flight refusals share no longer applies, because those refusals
// each guard exactly one running stage, not a CLI entry point with seven.
func TestDeployLaunchDeferredRecordsNothingAcrossEverySource(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	found := false
	for _, e := range doc.Events {
		if e.Name != "deploy-launch-deferred" {
			continue
		}
		found = true
		assert.Contains(t, e.Src, "reviewed")
		assert.Equal(t, "reviewed", e.Dst)
		assert.False(t, e.Moves(), "deploy-launch-deferred must declare moves_item: false")
		assert.Empty(t, e.Marker, "the absence of a marker IS the fix")
		assert.NotEmpty(t, e.Doc, "say why it records nothing")
		assert.Contains(t, e.Where, "deploy_entry.go", "name the CLI's own entry point")
	}
	assert.True(t, found, "deploy-launch-deferred is missing from the document")
}
