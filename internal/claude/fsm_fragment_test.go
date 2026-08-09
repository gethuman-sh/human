package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/pipelinefsm"
)

func promptBodies(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir("embed")
	require.NoError(t, err)
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("embed", e.Name()))
		require.NoError(t, err)
		out[e.Name()] = string(body)
	}
	return out
}

// The rule the stage-lease include already establishes: every agent the board
// can launch carries it. An agent that can be launched is an agent that can get
// stuck, and one never told it may ask will stop and ask a person instead —
// the outcome the pipeline exists to avoid.
func TestFSMFragment_EveryBoardLaunchedAgentCarriesIt(t *testing.T) {
	found := 0
	for name, body := range promptBodies(t) {
		// The stage-lease include is the existing marker of "the board can launch
		// this", so the two sets stay identical by construction rather than by a
		// second list here that would drift from it.
		if !strings.Contains(body, "human:include stage-lease") {
			continue
		}
		found++
		assert.Contains(t, body, "<!-- human:include fsm -->",
			"%s is board-launched but is never told it can ask the machine", name)
	}
	assert.Positive(t, found, "some prompt is board-launched, or this test proves nothing")
}

// The fragment takes no argument, and that must stay true.
//
// An earlier draft bound each prompt to the state its agent normally occupies.
// The premise was wrong — an agent knows its ticket key and not its state, which
// is the thing it is asking — and it was wrong in practice on the first try: the
// bug fixer was bound to `preflight` when it is `fixing`, and a test that only
// checked the state EXISTED could not catch it. A binding that cannot be
// verified against the run it describes should not exist.
func TestFSMFragment_TakesNoStateArgument(t *testing.T) {
	assert.NotContains(t, fragmentArgs, "fsm",
		"the fsm fragment must take no argument — a prompt cannot know the state its run will be in")

	for name, body := range promptBodies(t) {
		assert.NotContains(t, body, "human:include fsm state=",
			"%s binds the fragment to a state; ask with the ticket key instead", name)
	}
}

// The fragment must point at the command that answers from what an agent holds.
func TestFSMFragment_PointsAtWhere(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("embed", "shared", "fsm.md"))
	require.NoError(t, err)
	text := string(body)

	assert.Contains(t, text, "human fsm where", "where is the command with the value")
	assert.Contains(t, text, `"yours": true`,
		"the rule that stops an agent taking an edge it does not own must be stated")
}

// A state named in the fragment's prose must exist, or it sends an agent to ask
// about something the machine no longer has.
func TestFSMFragment_NamesNoStaleState(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join("embed", "shared", "fsm.md"))
	require.NoError(t, err)

	for _, name := range doc.StateNames() {
		assert.NotContains(t, string(body), "fsm where "+name,
			"the fragment shows %q where a ticket key belongs", name)
	}
}
