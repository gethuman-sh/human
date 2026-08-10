//go:build wailsapp

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/claude/logparser"
	"github.com/gethuman-sh/human/internal/claude/monitor"
)

// SC-3603: the frontend's normalization door (instancesFromPayload in
// board.ts/board.js) is the backstop against a bad payload blanking the
// pane, but the producer side should not lean on it — Go's zero value for a
// slice marshals to `null`, which is one of the two payloads that throws
// deep in the pane's row renderers. These tests pin the hand-written
// empty-slice literals in instances.go that keep the wire payload an
// array, never null, so the hazard the frontend door guards against is not
// reintroduced here with no test noticing.

// TestAgentInstanceFromView_listsMarshalAsEmptyArraysNotNull pins the
// Subagents empty-slice literal in agentInstanceFromView: a session-less
// instance (the common case — a process discovered but no session log
// matched yet) must still marshal its list fields as `[]`, not `null`.
func TestAgentInstanceFromView_listsMarshalAsEmptyArraysNotNull(t *testing.T) {
	ai := agentInstanceFromView(monitor.InstanceView{})
	b, err := json.Marshal(ai)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, `"models":[]`) {
		t.Errorf("expected \"models\":[] in %s", got)
	}
	if !strings.Contains(got, `"subagents":[]`) {
		t.Errorf("expected \"subagents\":[] in %s", got)
	}
	if strings.Contains(got, "null") {
		t.Errorf("expected no null field in %s", got)
	}
}

// TestModelUsages_nilSummaryMarshalsAsEmptyArray pins the out := []ModelUsage{}
// literal: a nil usage summary (no models seen yet) must not marshal as null.
func TestModelUsages_nilSummaryMarshalsAsEmptyArray(t *testing.T) {
	b, err := json.Marshal(modelUsages(nil))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("modelUsages(nil) marshaled to %s, want []", string(b))
	}
}

// TestInstancesData_emptyAgentsMarshalsAsEmptyArray pins the constructor
// literal in Instances() (data := InstancesData{Agents: []AgentInstance{}})
// against the zero value, which is the hazard it exists to avoid: an
// InstancesData built without that literal marshals Agents as null, and
// that is one of the two payload shapes the frontend door
// (instancesFromPayload, src/board.ts) exists to survive.
func TestInstancesData_emptyAgentsMarshalsAsEmptyArray(t *testing.T) {
	b, err := json.Marshal(InstancesData{Agents: []AgentInstance{}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != `{"agents":[]}` {
		t.Errorf("got %s, want {\"agents\":[]}", string(b))
	}

	// The documented hazard: the zero value has no such literal, so a future
	// caller that builds InstancesData{} directly (bypassing Instances())
	// reintroduces the null payload. This assertion exists to make that
	// concrete, not to bless it — it is what SC-3603's frontend door guards.
	zero, err := json.Marshal(InstancesData{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(zero) != `{"agents":null}` {
		t.Errorf("got %s, want {\"agents\":null} (the hazard the constructor literal avoids)", string(zero))
	}
}

// TestApplySessionFields_populatesSubagentsWithoutNil exercises the mapper
// with a real session, asserting the subagent list is populated (not nil)
// and the task counters split correctly.
func TestApplySessionFields_populatesSubagentsWithoutNil(t *testing.T) {
	started := time.Now().Add(-time.Minute)
	sess := &logparser.SessionState{
		Status:    logparser.StatusWorking,
		StartedAt: started,
		Subagents: []logparser.Subagent{
			{Description: "explore the repo", SubagentType: "Explore", StartedAt: started},
		},
		Tasks: []logparser.Task{
			{Status: "completed"},
			{Status: "pending"},
		},
	}

	ai := &AgentInstance{}
	applySessionFields(ai, sess)

	if ai.Subagents == nil {
		t.Fatal("Subagents is nil, want a populated slice")
	}
	if len(ai.Subagents) != 1 {
		t.Fatalf("len(Subagents) = %d, want 1", len(ai.Subagents))
	}
	if ai.TasksDone != 1 {
		t.Errorf("TasksDone = %d, want 1", ai.TasksDone)
	}
	if ai.TasksPending != 1 {
		t.Errorf("TasksPending = %d, want 1", ai.TasksPending)
	}
	if ai.TasksInProgress != 0 {
		t.Errorf("TasksInProgress = %d, want 0", ai.TasksInProgress)
	}
}
