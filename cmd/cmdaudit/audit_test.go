package cmdaudit

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/audit"
)

func TestBuildAuditCmd_subcommands(t *testing.T) {
	cmd := BuildAuditCmd()
	assert.Equal(t, "audit", cmd.Name())
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	assert.True(t, names["list"], "list subcommand registered")
	assert.True(t, names["show"], "show subcommand registered")
}

func TestBuildFilterArgs(t *testing.T) {
	t.Run("allEmpty", func(t *testing.T) {
		assert.Nil(t, buildFilterArgs("", "", "", "", 0))
	})
	t.Run("populated", func(t *testing.T) {
		got := buildFilterArgs("2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z", "KAN-1", "jira", 5)
		assert.Equal(t, []string{
			"--since", "2026-01-01T00:00:00Z",
			"--until", "2026-02-01T00:00:00Z",
			"--subject", "KAN-1",
			"--tracker", "jira",
			"--limit", "5",
		}, got)
	})
	t.Run("limitZeroOmitted", func(t *testing.T) {
		got := buildFilterArgs("", "", "KAN-2", "", 0)
		assert.Equal(t, []string{"--subject", "KAN-2"}, got)
	})
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "ab…", truncate("abcdef", 3))
	assert.Equal(t, "a", truncate("abcdef", 1))
	assert.Equal(t, "", truncate("abc", 0))
}

func TestRenderTable_empty(t *testing.T) {
	var b bytes.Buffer
	assert.NoError(t, renderTable(&b, nil))
	assert.Contains(t, b.String(), "no audit events")
}

func TestRenderTable_rows(t *testing.T) {
	var b bytes.Buffer
	events := []audit.Event{
		{
			Source:  "human/daemon/jira",
			Type:    "sh.human.tracker.create",
			Subject: "KAN-1",
			Data: audit.Data{
				Outcome:  audit.OutcomeSuccess,
				Decision: audit.DecisionContext{Rationale: "implementing plan"},
			},
		},
	}
	assert.NoError(t, renderTable(&b, events))
	out := b.String()
	assert.Contains(t, out, "OUTCOME")
	assert.Contains(t, out, "KAN-1")
	assert.Contains(t, out, "success")
	assert.Contains(t, out, "implementing plan")
}

func TestWriteJSON(t *testing.T) {
	var b bytes.Buffer
	assert.NoError(t, writeJSON(&b, map[string]string{"k": "v"}))
	assert.Contains(t, b.String(), "\"k\": \"v\"")
}
