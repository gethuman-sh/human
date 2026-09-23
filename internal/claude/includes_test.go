package claude

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/marker"
)

func TestExpandIncludes_SubstitutesAKnownFragment(t *testing.T) {
	in := []byte("# Agent\n\n<!-- human:include exit-contract -->\n\nrest\n")

	out, err := expandIncludes(in)
	require.NoError(t, err)
	require.Contains(t, string(out), "How this run may end")
	require.Contains(t, string(out), "needs-human-work")
	require.NotContains(t, string(out), "human:include", "the directive itself must be consumed")
	require.Contains(t, string(out), "rest", "surrounding content survives")
}

// The exit contract must give agents the vocabulary to record a substrate
// outage as its own exit class, distinct from a retryable flake (SC-2307).
func TestExpandIncludes_ExitContractHasOutageRow(t *testing.T) {
	in := []byte("<!-- human:include exit-contract -->\n")

	out, err := expandIncludes(in)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "`outage`", "the outage exit class must be documented")
	require.Contains(t, s, "five ways", "the contract now enumerates five endings, not four")
	require.NotContains(t, s, "a network blip",
		"retryable must no longer claim substrate/network failures — those are now an outage")
}

func TestExpandIncludes_LeavesContentWithoutDirectivesAlone(t *testing.T) {
	in := []byte("# Agent\n\nplain prompt\n")

	out, err := expandIncludes(in)
	require.NoError(t, err)
	require.Equal(t, string(in), string(out))
}

// A dangling directive would silently drop a rule the pipeline depends on and
// only surface much later as an agent behaving oddly, so it fails the install.
func TestExpandIncludes_UnknownFragmentIsAnError(t *testing.T) {
	_, err := expandIncludes([]byte("<!-- human:include no-such-fragment -->\n"))

	require.Error(t, err)
	// The offending name travels as a structured detail, not in the message.
	require.Equal(t, "no-such-fragment", errors.AllDetails(err)["fragment"])
}

func TestExpandIncludes_HandlesRepeatedAndIndentedDirectives(t *testing.T) {
	in := []byte("<!-- human:include exit-contract -->\n\nmiddle\n\n  <!-- human:include exit-contract -->\n")

	out, err := expandIncludes(in)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(out), "How this run may end"))
}

// An inline mention inside a sentence is not a directive: only a whole line is.
func TestExpandIncludes_IgnoresInlineMentions(t *testing.T) {
	in := []byte("Write `<!-- human:include exit-contract -->` to pull in the contract.\n")

	out, err := expandIncludes(in)
	require.NoError(t, err)
	require.Equal(t, string(in), string(out))
}

// Guards the whole shipped prompt set: a mistyped directive in any embedded
// skill or agent must fail here, not silently reach a user's .claude directory.
func TestInstall_ExpandsEveryEmbeddedDirective(t *testing.T) {
	fw := newMockFileWriter()
	var buf bytes.Buffer

	require.NoError(t, Install(&buf, fw, false))

	carriers := 0
	for name, body := range fw.files {
		require.NotContains(t, string(body), "human:include",
			"%s shipped with an unexpanded directive", name)
		if strings.Contains(string(body), "How this run may end") {
			carriers++
		}
	}
	require.Positive(t, carriers, "no installed prompt carries the exit contract")
}

// Every fragment must be non-empty: an empty one would expand to nothing and
// silently remove the rule it is supposed to carry.
func TestSharedFragments_AreAllPopulated(t *testing.T) {
	require.NotEmpty(t, sharedFragments)
	for name, body := range sharedFragments {
		require.NotEmpty(t, strings.TrimSpace(string(body)), "fragment %q is empty", name)
	}
}

// The needs-human-work row must carry the blocker contract: a stop the machine
// takes on an agent's word records what was observed, what was tried and what
// would release it, and names its marker concretely (SC-5179).
func TestExpandIncludes_ExitContractHasBlockerContract(t *testing.T) {
	out, err := expandIncludes([]byte("<!-- human:include exit-contract -->\n"))
	require.NoError(t, err)
	s := string(out)
	for _, want := range []string{"`kind`", "`evidence`", "`attempted`", "`release`",
		"`missing-permission`", "`unavailable-dependency`", "`exhausted-fix-rounds`", "`conflicting-requirements`", "`other`",
		"check what can be checked", `"blocker":{"kind":"missing-permission"`,
		"`planning-failed`", "`implementation-failed`", "`review-failed`", "`deploy-failed`"} {
		require.Contains(t, s, want)
	}
	require.NotContains(t, s, "<stage>-failed", "the marker name is concrete; a placeholder posts a comment the board never classifies")
}

// The exit contract's prose lists the blocker kind vocabulary independently of
// marker.BlockerKinds() (each pinned by its own test), so nothing catches the
// two drifting apart. Driving the prose assertion from the enum itself closes
// that gap: adding or renaming a kind in code without updating the doc fails
// here (SC-5250).
func TestExpandIncludes_ExitContractListsEveryBlockerKind(t *testing.T) {
	out, err := expandIncludes([]byte("<!-- human:include exit-contract -->\n"))
	require.NoError(t, err)
	s := string(out)
	for _, kind := range marker.BlockerKinds() {
		require.Contains(t, s, "`"+kind+"`", "exit-contract.md is missing the blocker kind %q", kind)
	}
}
