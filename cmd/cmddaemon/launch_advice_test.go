package cmddaemon

import (
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/recall"
)

// A daemon whose findings record could not be opened runs every launch
// without a briefing and every PR round without a record — both as nil
// interfaces, never as a typed nil that passes a nil check and panics.
func TestLaunchAdvice_noRecordDisablesEverything(t *testing.T) {
	assert.Nil(t, newLaunchAdvice(nil, newClaudeAuthRefusals(), zerolog.Nop()))
	var none *launchAdvice
	assert.Nil(t, none.feedbackFor(daemon.ProjectEntry{Dir: t.TempDir()}, "", nil, nil))
	assert.Nil(t, findingsRecorder(nil))
}

func TestLaunchAdvice_oneCachePerProjectDir(t *testing.T) {
	store, err := recall.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a := newLaunchAdvice(store, newClaudeAuthRefusals(), zerolog.Nop())
	require.NotNil(t, a)
	assert.Same(t, a.cacheFor("/a"), a.cacheFor("/a"))
	assert.NotSame(t, a.cacheFor("/a"), a.cacheFor("/b"))
	assert.NotNil(t, findingsRecorder(store))

	advice := a.feedbackFor(daemon.ProjectEntry{Dir: t.TempDir()}, "", nil, nil)
	require.NotNil(t, advice)
	assert.Empty(t, advice(context.Background(), "SC-1", daemon.BoardPlanning, ""), "an empty record makes no call and no block")
}

// The feedback route and the launch path must be one constructor: a nil
// advice disables both, and a live one answers the route from the same
// per-project cache a launch fills.
func TestLaunchAdvice_explainerFollowsTheLaunchPath(t *testing.T) {
	var none *launchAdvice
	assert.Nil(t, none.explainerFor(nil, nil))

	store, err := recall.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a := newLaunchAdvice(store, newClaudeAuthRefusals(), zerolog.Nop())
	entry := daemon.ProjectEntry{Dir: t.TempDir()}
	assert.Same(t, a.cacheFor(entry.Dir), a.depsFor(entry, "", nil, nil).Cache)

	reg, err := daemon.NewProjectRegistry(nil)
	require.NoError(t, err)
	explain := a.explainerFor(reg, nil)
	require.NotNil(t, explain)
	_, err = explain(daemon.FeedbackRequest{Key: "SC-1", Stage: daemon.BoardPlanning})
	require.Error(t, err, "a key no registered project owns is refused, not answered from nowhere")
}

// The briefing turn must see the record and nothing else, and must not
// reason its way past the launch budget (SC-6039): no project directory, no
// settings (hence no hooks), no MCP servers, no session file, the answer's
// shape fixed by the system prompt, and the reasoning phase switched off.
func TestHostFeedbackRunner_turnIsContextFreeAndUnreasoned(t *testing.T) {
	assert.Contains(t, feedbackTurnEnv, "MAX_THINKING_TOKENS=0")
	joined := strings.Join(feedbackTurnArgs, "\x00")
	for _, flag := range []string{"--setting-sources\x00", "--strict-mcp-config", "--mcp-config\x00{\"mcpServers\":{}}", "--no-session-persistence", "--system-prompt\x00" + feedbackSystemPrompt} {
		assert.Contains(t, joined, flag)
	}
	assert.Contains(t, feedbackSystemPrompt, "NONE", "the sentinel the cleaner looks for is the one the model is told")
	assert.Contains(t, feedbackSystemPrompt, `"- "`, "the line prefix the cleaner keeps is the one the model is told")
}
