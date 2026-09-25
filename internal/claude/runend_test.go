package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rule is asserted on the one fragment, not per member: the family registry
// already proves all three carry the include, and expansion makes their copies
// identical by construction.
func TestRunEndFragmentCarriesTheRule(t *testing.T) {
	content := string(sharedFragments["run-end"])

	assert.Contains(t, content, "Your run ends at its own summary marker.")
	assert.Contains(t, content, "Never post a plain, un-markered ticket comment")
	assert.Contains(t, content, "Markers posted by later stages are not your business.")
	assert.Contains(t, content, "still holding the checkout")
}

// SC-5691: the board stop moved to the inline review (7.3) when the review moved
// into this container, and both fix skills still called it "after the handoff
// (7.1)" — contradicting their own overview line.
func TestFixSkillsNameTheBoardStopAfterTheInlineReview(t *testing.T) {
	for _, name := range []string{"human-autofix-skill.md", "human-security-fix-skill.md"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("embed", name))
			require.NoError(t, err)
			body := string(raw)
			assert.NotContains(t, body, "after the handoff (7.1)",
				"the board-context stop is after the inline review, not after the handoff")
			assert.Contains(t, body, "the board-context stop after the inline review (7.3)")
		})
	}
}

// The deploy's non-failure refusals are a closed set, and a fix run that meets
// one must know it is not a failure. A fourth was added (SC-5691).
func TestFixSkillsKnowEveryDeployRefusal(t *testing.T) {
	for _, name := range []string{"human-autofix-skill.md", "human-security-fix-skill.md"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("embed", name))
			require.NoError(t, err)
			assert.Contains(t, string(raw), "deploy refused: the implementation container for this ticket still holds the checkout")
		})
	}
}
