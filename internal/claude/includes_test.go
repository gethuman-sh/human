package claude

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
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
