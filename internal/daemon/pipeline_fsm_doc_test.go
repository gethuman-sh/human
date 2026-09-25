package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// internal/pipelinefsm/pipeline-fsm.json is the written-down machine. These tests are what stop
// it drifting from the CODE: a marker the prompts post that the daemon has never
// been told about, a marker string no constant carries.
//
// Whether the document is a well-formed machine at all — dangling destinations,
// unreachable states, states with no way out — is a question about the document
// alone, and lives in internal/pipelinefsm (and the fsmcheck command). Two
// questions, two checks: this one needs the daemon's constants and so has to live
// here; that one needs nothing but the file and so should not.
//
// Kept as a test rather than a separate tool so `make check` runs it and a
// change to the machine cannot merge without the description following it.

type fsmDoc struct {
	Initial string `json:"initial"`
	States  []struct {
		Name             string `json:"name"`
		Terminal         bool   `json:"terminal"`
		Holds            string `json:"holds"`
		IfNothingHappens string `json:"if_nothing_happens"`
	} `json:"states"`
	Events []struct {
		Name      string   `json:"name"`
		Src       []string `json:"src"`
		Dst       string   `json:"dst"`
		Actor     string   `json:"actor"`
		Marker    string   `json:"marker"`
		Where     string   `json:"where"`
		Guard     string   `json:"guard"`
		Doc       string   `json:"doc"`
		MovesItem *bool    `json:"moves_item"`
	} `json:"events"`
	Unclassified struct {
		Markers []string `json:"markers"`
	} `json:"unclassified_markers"`
	Invariants struct {
		Constants map[string]string `json:"constants"`
	} `json:"invariants"`
}

func loadFSMDoc(t *testing.T) fsmDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "pipelinefsm", "pipeline-fsm.json"))
	require.NoError(t, err, "the pipeline machine must be readable")
	var doc fsmDoc
	require.NoError(t, json.Unmarshal(raw, &doc), "the pipeline machine must be valid JSON")
	require.NotEmpty(t, doc.States)
	require.NotEmpty(t, doc.Events)
	return doc
}

// Marker strings in the document must be real. Checked against the daemon's own
// header constants rather than a grep, so a renamed constant fails here instead
// of leaving the document quietly describing a marker that no longer exists.
func TestPipelineFSM_MarkersExist(t *testing.T) {
	doc := loadFSMDoc(t)
	known := map[string]bool{}
	for _, h := range []string{
		TicketReviewStartedHeader, TicketReviewedHeader,
		PlanningStartedHeader, PlanReadyHeader, PlanningFailedHeader, NothingToDoHeader,
		ImplementationStartedHeader, ImplementationFailedHeader, NeedsPlanningHeader, NoFixNeededHeader,
		ReadyForReviewHeader, ReviewStartedHeader, ReviewCompleteHeader, ReviewFailedHeader,
		PRStartedHeader, PRPushedHeader, PRFailedHeader,
		DeployStartedHeader, DeployedHeader, DeployFailedHeader,
		PRReviewStartedHeader, PRFixStartedHeader, PRReviewFailedHeader, PRReviewPassedHeader,
		DeployFixStartedHeader,
		PlanningOutageHeader, ImplementationOutageHeader, ReviewOutageHeader, DeployOutageHeader,
		PlanCommentHeader, CloseFailedHeader, RelatedStartedHeader, RelatedHeader,
		ShippedPartialHeader, BugVerdictHeader, BugVerifyHeader, PipelineStartedHeader, HandoffCheckUnreadableHeader,
		OptionsHeader, OptionChosenHeader, ClaimHeader,
	} {
		known[h] = true
	}
	for _, e := range doc.Events {
		if e.Marker == "" {
			continue
		}
		// One transition may record any of several per-stage markers.
		for _, m := range strings.Split(e.Marker, " | ") {
			require.True(t, known[strings.TrimSpace(m)],
				"%s: marker %q is not a marker this daemon defines", e.Name, m)
		}
	}
}

// The rule that catches the most, and the one that would have caught bug-verify:
// every marker an agent prompt is told to post must be EITHER a transition in
// the document OR listed as deliberately not one. Never neither — a marker in
// neither list is one the prompts treat as meaningful and the code has never
// been told about.
func TestPipelineFSM_EveryPromptedMarkerIsAccountedFor(t *testing.T) {
	doc := loadFSMDoc(t)

	accounted := map[string]bool{}
	for _, e := range doc.Events {
		for _, m := range strings.Split(e.Marker, " | ") {
			m = strings.TrimSpace(m)
			m = strings.TrimSuffix(strings.TrimPrefix(m, "[human:"), "]")
			if m != "" {
				accounted[m] = true
			}
		}
	}
	for _, m := range doc.Unclassified.Markers {
		accounted[m] = true
	}
	// The handoff is posted by `human handoff post`, not `human marker post`.
	accounted["ready-for-review"] = true

	dir := filepath.Join("..", "claude", "embed")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	posts := regexp.MustCompile(`marker post [^ ]+ ([a-z][a-z-]+)`)

	var orphans []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		for _, m := range posts.FindAllStringSubmatch(string(body), -1) {
			if !accounted[m[1]] {
				orphans = append(orphans, m[1]+" (posted by "+entry.Name()+")")
			}
		}
	}
	sort.Strings(orphans)
	require.Empty(t, orphans,
		"these markers are posted by a prompt but are neither a transition nor listed as deliberately not one — "+
			"decide which they are and record it in internal/pipelinefsm/pipeline-fsm.json")
}

// SC-5691: the deploy works the same checkout the implementation container
// holds, and since SC-782 the verdict is posted from inside that container
// minutes before it exits. The document said nothing about it —
// reviewed.if_nothing_happens waited only "for the Deploy gesture" — so the
// guard has to be in the prose an agent is served (`human fsm where`) as well
// as in the code.
func TestPipelineFSM_ReviewedGuardsTheCheckoutAgainstTheImplementationContainer(t *testing.T) {
	doc := loadFSMDoc(t)

	var reviewed struct{ holds, ifNothing string }
	for _, s := range doc.States {
		if s.Name == "reviewed" {
			reviewed.holds, reviewed.ifNothing = s.Holds, s.IfNothingHappens
		}
	}
	require.NotEmpty(t, reviewed.ifNothing, "the document must declare a reviewed state")
	assert.Contains(t, reviewed.ifNothing, "implementation container",
		"reviewed must say the Deploy waits for the container that holds the checkout")
	assert.Contains(t, reviewed.ifNothing, "DeployCheckoutWaitBound",
		"the wait must name its bound, so a reader is told the number instead of the name")
	assert.Contains(t, reviewed.holds, "SC-782",
		"reviewed must say why a verdict is not evidence the checkout is free")

	found := false
	for _, e := range doc.Events {
		if e.Name != "deploy-launch-deferred" {
			continue
		}
		found = true
		require.Equal(t, []string{"reviewed"}, e.Src)
		assert.Equal(t, "reviewed", e.Dst, "a deferred launch moves nothing")
		require.NotNil(t, e.MovesItem)
		assert.False(t, *e.MovesItem, "it must declare moves_item: false")
		assert.Empty(t, e.Marker, "the absence of a marker IS the behaviour")
		assert.Contains(t, e.Where, "awaitCheckoutFree", "name the code that defers")
	}
	assert.True(t, found, "deploy-launch-deferred is missing from the document")

	for _, name := range []string{"start-pr-review", "redeploy-after-outage", "start-deploy"} {
		for _, e := range doc.Events {
			if e.Name == name {
				assert.Contains(t, e.Guard, "checkout",
					"%s enters the done stage, so it must declare the checkout interlock as a guard", name)
			}
		}
	}

	assert.Contains(t, doc.Invariants.Constants["DeployCheckoutWaitBound"], "15m",
		"the document's budget must be the code's budget")
	assert.Equal(t, 15*time.Minute, DeployCheckoutWaitBound,
		"the code's budget must be the document's budget")
}

// SC-5793: every planning start classifies the ticket's pipeline first, and the
// document has to say so — `human fsm where` serves this prose to the agents and
// to a person reading a stuck card, and a planning transition that silently means
// "feature" is the bug this ticket fixed.
func TestPipelineFSM_PlanningTransitionsNameTheFixClassification(t *testing.T) {
	doc := loadFSMDoc(t)
	byName := map[string]struct {
		src    []string
		dst    string
		doc    string
		guard  string
		where  string
		actor  string
		marker string
	}{}
	for _, e := range doc.Events {
		byName[e.Name] = struct {
			src    []string
			dst    string
			doc    string
			guard  string
			where  string
			actor  string
			marker string
		}{e.Src, e.Dst, e.Doc, e.Guard, e.Where, e.Actor, e.Marker}
	}

	for _, name := range []string{"start-planning", "reopen-planning", "launch-refused-no-plan"} {
		e, ok := byName[name]
		require.True(t, ok, "%s is missing from the document", name)
		assert.Contains(t, e.guard, "classifyFixPipeline",
			"%s: the classification is a guard on this transition, not a footnote", name)
		assert.Contains(t, e.doc, "SC-5793", "%s: say why the classification is there", name)
	}

	resumed, ok := byName["fix-resumed-instead-of-planning"]
	require.True(t, ok, "the transition a classified planning start takes is missing")
	assert.Equal(t, "preflight", resumed.dst)
	assert.Equal(t, "daemon", resumed.actor)
	assert.Equal(t, ImplementationStartedHeader, resumed.marker)
	assert.Contains(t, resumed.where, "refuseIfUnplanned")
	assert.Contains(t, resumed.where, "launchPlanningOrFix")
	assert.ElementsMatch(t, []string{"implementing", "stopped", "substrate-down", "queued"}, resumed.src)

	assert.Contains(t, byName["start-fix-run"].src, "nothing-to-do",
		"re-opening a nothing-to-do on a bug resumes the fix pipeline")

	// A guard that can fail leaves the item somewhere, and the document has to
	// say where: every state a classified planning transition may start from
	// must also start a fix-resuming transition, or the machine describes only
	// the feature half of a fork the code takes both ways (SC-5793).
	resumeSrc := map[string]bool{}
	for _, name := range []string{"start-fix-run", "fix-resumed-instead-of-planning"} {
		for _, s := range byName[name].src {
			resumeSrc[s] = true
		}
	}
	for _, name := range []string{"start-planning", "reopen-planning"} {
		for _, s := range byName[name].src {
			assert.True(t, resumeSrc[s],
				"%s may start from %q, but no fix-resuming transition does: the classification's other branch is undescribed", name, s)
		}
	}
}
