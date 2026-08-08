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
