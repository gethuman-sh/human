package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/pipelinefsm"
)

var fsmIncludeRe = regexp.MustCompile(`human:include fsm state=([a-z0-9-]+)`)

// A prompt telling an agent "you are in <state>" is only useful while that state
// exists. Rename or drop one in the machine and the include silently starts
// naming nothing — the agent would then run `human fsm next <gone>`, get an
// error, and fall back to the guessing this fragment exists to replace.
func TestFSMFragment_EveryPromptNamesADeclaredState(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	entries, err := os.ReadDir("embed")
	require.NoError(t, err)

	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("embed", e.Name()))
		require.NoError(t, err)
		for _, m := range fsmIncludeRe.FindAllStringSubmatch(string(body), -1) {
			found++
			_, ok := doc.FindState(m[1])
			assert.True(t, ok, "%s: includes the fsm fragment for state %q, which the machine does not declare (known: %s)",
				e.Name(), m[1], strings.Join(doc.StateNames(), ", "))
		}
	}
	assert.Positive(t, found, "some prompt carries the fragment, or this test proves nothing")
}

// The rule the stage-lease include already follows: every agent the board can
// launch carries it. An agent that can be launched is an agent that can get
// stuck, and one that was never told it may ask will stop and ask a person
// instead — the outcome the whole pipeline exists to avoid.
func TestFSMFragment_EveryBoardLaunchedAgentCarriesIt(t *testing.T) {
	entries, err := os.ReadDir("embed")
	require.NoError(t, err)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("embed", e.Name()))
		require.NoError(t, err)
		// The stage-lease include is the existing marker of "the board can launch
		// this", so the two sets are kept identical by construction rather than by
		// a second list here that would drift from it.
		if !strings.Contains(string(body), "human:include stage-lease") {
			continue
		}
		assert.Regexp(t, fsmIncludeRe, string(body),
			"%s is board-launched but is never told it can ask the machine", e.Name())
	}
}

// The fragment must not ship a usable example state, for the reason stage-lease
// learned the hard way: nine prompts copied its worked example instead of
// substituting their own, and every one of them was then wrong.
func TestFSMFragment_CarriesNoUsableExampleState(t *testing.T) {
	doc, err := pipelinefsm.Load()
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join("embed", "shared", "fsm.md"))
	require.NoError(t, err)
	text := string(body)

	assert.Contains(t, text, "<STATE>", "the placeholder is what the include binds")
	for _, name := range doc.StateNames() {
		assert.NotContains(t, text, "fsm next "+name,
			"the fragment shows %q as a worked example, which prompts copy verbatim", name)
	}
}
