package agentname

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoard_RoundTrips(t *testing.T) {
	cases := []struct{ key, stage, want string }{
		{"SC-105", "implementation", "board-SC-105-implementation"},
		{"SC-1", "prreview", "board-SC-1-prreview"},
		{"octocat/repo#42", "planning", "board-octocat-repo-42-planning"},
	}
	for _, c := range cases {
		name := Board(c.key, c.stage)
		assert.Equal(t, c.want, name)

		key, stage, ok := ParseBoard(name)
		require.True(t, ok, name)
		assert.Equal(t, SanitizeKey(c.key), key, "the key comes back in the form the name carries")
		assert.Equal(t, c.stage, stage)
	}
}

func TestIsBoard(t *testing.T) {
	assert.True(t, IsBoard("board-SC-1-implementation"))
	assert.False(t, IsBoard("agent-SC-1"))
	assert.False(t, IsBoard(""))
	// The prefix alone is a board name by this test and no more: it carries no
	// key or stage, which ParseBoard is what refuses.
	assert.True(t, IsBoard(BoardPrefix))
	_, _, ok := ParseBoard(BoardPrefix)
	assert.False(t, ok)
}

func TestParseBoard_RefusesWhatIsNotOne(t *testing.T) {
	for _, name := range []string{"", "board-", "SC-1-implementation", "board--implementation", "board-SC-1-"} {
		_, _, ok := ParseBoard(name)
		assert.False(t, ok, name)
	}
}

// A two-token name is not refused: the grammar cannot tell "key SC, stage 1"
// from a truncated name, and it does not try. Pinned because it is what the
// parse has always done, not because it is desirable — a caller that cares
// checks the stage against the ones it knows.
func TestParseBoard_TwoTokensParseAsKeyAndStage(t *testing.T) {
	key, stage, ok := ParseBoard("board-SC-1")
	require.True(t, ok)
	assert.Equal(t, "SC", key)
	assert.Equal(t, "1", stage)
}

// The stage token must carry no hyphen: the parse splits on the LAST one, so a
// hyphenated token takes part of itself for the key. This is the invariant the
// daemon's hyphen-free stage constants exist to satisfy — pinned here, beside
// the parse that depends on it, rather than only in a comment beside them.
func TestParseBoard_SplitsOnTheLastHyphen(t *testing.T) {
	key, stage, ok := ParseBoard(Board("SC-1", "pr-review"))
	require.True(t, ok)
	assert.Equal(t, "SC-1-pr", key, "a hyphenated stage token loses its head to the key")
	assert.Equal(t, "review", stage)
}

func TestSanitizeKey(t *testing.T) {
	assert.Equal(t, "SC-105", SanitizeKey("SC-105"))
	assert.Equal(t, "octocat-repo-42", SanitizeKey("octocat/repo#42"))
	assert.Equal(t, "a-b", SanitizeKey("a   b"), "a run of invalid characters collapses to one hyphen")
}
