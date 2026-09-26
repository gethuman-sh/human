package cmddaemon

import (
	"context"
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
