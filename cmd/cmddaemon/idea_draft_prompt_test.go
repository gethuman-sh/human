package cmddaemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/daemon"
)

// The recreate flag has to survive all the way to the skill's invocation: the
// guard bypass lives in the CLI the skill calls, so a prompt that drops the
// flag leaves the board's Recreate item launching a run that stands down.
func TestIdeaDraftPrompt_CarriesTheRecreateFlag(t *testing.T) {
	assert.Equal(t, "/human-idea-draft SC-1",
		ideaDraftPrompt(daemon.IdeaDraftRequest{Key: "SC-1", Title: "an idea"}))
	assert.Equal(t, "/human-idea-draft SC-1 --recreate",
		ideaDraftPrompt(daemon.IdeaDraftRequest{Key: "SC-1", Title: "an idea", Recreate: true}))
}
