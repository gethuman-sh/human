package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// docs/pipeline-fsm.json is the written-down machine. These tests are what stop
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
		Name     string `json:"name"`
		Terminal bool   `json:"terminal"`
	} `json:"states"`
	Events []struct {
		Name   string   `json:"name"`
		Src    []string `json:"src"`
		Dst    string   `json:"dst"`
		Marker string   `json:"marker"`
	} `json:"events"`
	Unclassified struct {
		Markers []string `json:"markers"`
	} `json:"unclassified_markers"`
}

func loadFSMDoc(t *testing.T) fsmDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "pipeline-fsm.json"))
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
			"decide which they are and record it in docs/pipeline-fsm.json")
}
