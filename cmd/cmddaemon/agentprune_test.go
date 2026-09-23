package cmddaemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/devcontainer"
)

// Debris is an exited agent container nobody records as running: a running
// record owns its container until the stop path runs, a foreign container
// is not ours to touch, and a live container is never debris (SC-5248).
func TestExitedAgentDebris(t *testing.T) {
	containers := []devcontainer.ContainerSummary{
		{ID: "a", Names: []string{"/human-agent-board-1-planning"}, State: "exited"},
		{ID: "b", Names: []string{"/human-agent-board-2-planning"}, State: "exited"},
		{ID: "c", Names: []string{"/human-agent-board-3-planning"}, State: "exited"},
		{ID: "d", Names: []string{"/human-agent-board-4-planning"}, State: "running"},
		{ID: "e", Names: []string{"/other-human-agent-x"}, State: "exited"},
	}
	metas := []agent.Meta{
		{Name: "board-1-planning", Status: agent.StatusRunning},
		{Name: "board-2-planning", Status: agent.StatusStopped},
	}

	var ids []string
	for _, c := range exitedAgentDebris(containers, metas) {
		ids = append(ids, c.ID)
	}
	assert.Equal(t, []string{"b", "c"}, ids)
}
