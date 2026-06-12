package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNextName_NoAgents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	assert.Equal(t, "agent-1", NextName())
}

func TestNextName_SkipsToNextFree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	assert.NoError(t, WriteMeta(Meta{Name: "agent-3", Status: StatusRunning}))
	assert.NoError(t, WriteMeta(Meta{Name: "custom-name", Status: StatusRunning}))

	assert.Equal(t, "agent-4", NextName())
}

func TestPromptForIssue(t *testing.T) {
	tests := []struct {
		name        string
		trackerKind string
		isBug       bool
		want        string
	}{
		{"bug wins over tracker kind", "shortcut", true, "/human-bug-plan KEY-1"},
		{"pm tracker plans", "shortcut", false, "/human-plan KEY-1"},
		{"engineering tracker executes", "linear", false, "/human-execute KEY-1"},
		{"unknown tracker executes", "jira", false, "/human-execute KEY-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, PromptForIssue(tt.trackerKind, tt.isBug, "KEY-1"))
		})
	}
}
