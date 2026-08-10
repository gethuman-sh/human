package devcontainer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An agent container's PID 1 is `sleep infinity`, which never calls wait(), so
// without an init every process the agent orphans stays defunct for the life of
// the container — measured at eight zombie git processes in one seven-hour
// container (SC-4281).
func TestBuildCreateOptions_RunsAnInitThatReapsOrphans(t *testing.T) {
	m := &Manager{}
	dir := t.TempDir()

	opts := m.buildCreateOptions(&DevcontainerConfig{}, dir, dir, "human-agent-board-SC-1-implementation", "img", "/workspace", "hash", nil, "", nil)

	assert.Equal(t, []string{"sleep", "infinity"}, opts.Cmd, "PID 1 is the command that cannot reap")
	assert.True(t, opts.Init, "an init must reap what PID 1 will not")
}
