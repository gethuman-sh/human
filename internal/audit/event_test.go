package audit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildEventFields(t *testing.T) {
	d := Data{
		Operation: "create",
		Actor:     Actor{TrackerKind: "jira", TrackerName: "work"},
		Resource:  Resource{Project: "KAN"},
		Outcome:   OutcomeSuccess,
	}
	e := buildEventWithID("id1", time.Now(), d)

	assert.Equal(t, "sh.human.tracker.create", e.Type)
	assert.Equal(t, "human/daemon/jira/work", e.Source)
	assert.Equal(t, "KAN", e.Subject)
	assert.Equal(t, SpecVersion, e.SpecVersion)
	assert.Equal(t, "application/json", e.DataContentType)
}

func TestBuildEventSourceWithoutTrackerName(t *testing.T) {
	d := Data{Operation: "delete", Actor: Actor{TrackerKind: "jira"}, Resource: Resource{Key: "KAN-1"}}
	e := buildEventWithID("id1", time.Now(), d)
	assert.Equal(t, "human/daemon/jira", e.Source)
}

func TestBuildEventSubjectKeyPreferred(t *testing.T) {
	d := Data{Operation: "edit", Actor: Actor{TrackerKind: "jira"}, Resource: Resource{Key: "KAN-1", Project: "KAN"}}
	e := buildEventWithID("id1", time.Now(), d)
	assert.Equal(t, "KAN-1", e.Subject)
}

func TestDecisionFromEnv(t *testing.T) {
	vals := map[string]string{
		"HUMAN_AUDIT_MODEL_ID":      "claude-opus-4",
		"HUMAN_AUDIT_MODEL_VERSION": "20260101",
		"HUMAN_AUDIT_INPUTS":        "ticket HUM-1",
		"HUMAN_AUDIT_RATIONALE":     "implementing the plan",
	}
	dc := DecisionFromEnv(func(k string) string { return vals[k] })
	assert.Equal(t, "claude-opus-4", dc.ModelID)
	assert.Equal(t, "20260101", dc.ModelVersion)
	assert.Equal(t, "ticket HUM-1", dc.Inputs)
	assert.Equal(t, "implementing the plan", dc.Rationale)
}

func TestBuildEventStripsCredentials(t *testing.T) {
	args := []string{"jira", "issue", "delete", "KAN-1", "--jira-key", "SECRET", "--linear-token=TOPSECRET"}
	op := MutatingOp{Operation: "delete", TrackerKind: "jira", Key: "KAN-1"}
	e, err := BuildEvent(time.Now(), op, OutcomeSuccess, DecisionContext{}, args)
	require.NoError(t, err)

	for _, a := range e.Data.Args {
		assert.NotContains(t, a, "SECRET")
		assert.NotContains(t, a, "TOPSECRET")
		assert.NotEqual(t, "--jira-key", a)
	}
	assert.Contains(t, e.Data.Args, "delete")
	assert.Contains(t, e.Data.Args, "KAN-1")
}

func TestBuildEventIDUnique(t *testing.T) {
	op := MutatingOp{Operation: "create", TrackerKind: "jira"}
	e1, err := BuildEvent(time.Now(), op, OutcomeSuccess, DecisionContext{}, nil)
	require.NoError(t, err)
	e2, err := BuildEvent(time.Now(), op, OutcomeSuccess, DecisionContext{}, nil)
	require.NoError(t, err)

	assert.NotEmpty(t, e1.ID)
	assert.NotEmpty(t, e2.ID)
	assert.NotEqual(t, e1.ID, e2.ID)
}
