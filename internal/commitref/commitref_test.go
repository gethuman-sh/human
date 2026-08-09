package commitref_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/commitref"
)

// The package's own verdict on every case in the shared corpus.
func TestGrammar_ReachesTheCorpusVerdict(t *testing.T) {
	for _, tc := range commitref.Corpus {
		t.Run(tc.Why, func(t *testing.T) {
			accepted := commitref.Exempt(tc.Message) || commitref.HasAny(tc.Message)
			assert.Equal(t, tc.Accepted, accepted, "message: %q", tc.Message)
		})
	}
}

// TestHook_ReachesTheSameVerdictAsTheGrammar is the point of the corpus.
//
// The commit-msg hook is a second spelling of this grammar and has to stay one:
// a hook that needs the binary built cannot be the thing that gates building it.
// Two spellings drift, and both directions are quiet — a form the hook accepts
// and the search does not is a commit nobody can find afterwards; a form the
// search accepts and the hook does not is a commit nobody could have made. So
// the hook is actually RUN here, over the same cases.
func TestHook_ReachesTheSameVerdictAsTheGrammar(t *testing.T) {
	hook := hookPath(t)
	if _, err := os.Stat(hook); err != nil {
		t.Skipf("commit-msg hook not present at %s", hook)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	// The hook asks git what is staged, to exempt a docs-only commit. Run it in
	// a repository of its own so the answer is "nothing" rather than whatever
	// the developer happens to have staged.
	repo := emptyRepo(t)

	for _, tc := range commitref.Corpus {
		t.Run(tc.Why, func(t *testing.T) {
			msgFile := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
			require.NoError(t, os.WriteFile(msgFile, []byte(tc.Message), 0o600))

			cmd := exec.Command("bash", hook, msgFile)
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()

			assert.Equal(t, tc.Accepted, err == nil,
				"the hook and the grammar disagree about %q\nhook said: %s", tc.Message, strings.TrimSpace(string(out)))
		})
	}
}

// Every form the rejection message advertises must be a form the hook accepts.
// Advertising one it rejects is the cruellest shape this bug takes: the user
// does exactly what they were told and is refused again.
func TestHook_AcceptsEveryFormItAdvertises(t *testing.T) {
	hook := hookPath(t)
	if _, err := os.Stat(hook); err != nil {
		t.Skipf("commit-msg hook not present at %s", hook)
	}
	source, err := os.ReadFile(hook) // #nosec G304 -- a fixed path inside the repo
	require.NoError(t, err)

	for _, form := range commitref.Forms() {
		assert.Contains(t, string(source), form.Example,
			"the hook's help must show the %s form the grammar accepts", form.Name)
		assert.True(t, commitref.HasAny(form.Example),
			"the grammar must accept the %s form it advertises", form.Name)
	}
}

// hookPath resolves the hook absolutely: it is run from a repository of its
// own, so a path relative to the package directory would not find it.
func hookPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", ".githooks", "commit-msg"))
	require.NoError(t, err)
	return abs
}

func emptyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = dir
	require.NoError(t, cmd.Run(), "git init")
	return dir
}

func TestKeys_PrefixedFirstThenNumericEachOnce(t *testing.T) {
	keys := commitref.Keys([]string{
		"[SC-79] [HUM-59] Add validation",
		"Fix the parser (Issue #123)",
		"[SC-79] Again",
	})
	assert.Equal(t, []string{"SC-79", "HUM-59", "123"}, keys)
}

// A short key must not find a longer one's commits. SC-5 matching every SC-57
// is a wrong answer that reads like a right one.
func TestGrepPattern_GuardsAgainstLongerKeys(t *testing.T) {
	prefixed := commitref.GrepPattern("SC-57")
	assert.Contains(t, prefixed, `\[SC-57\]`)
	assert.NotContains(t, prefixed, `#?SC-57\]`,
		"a prefixed key must not carry the numeric hash form")

	assert.Contains(t, commitref.GrepPattern("42"), `\[#?42\]`,
		"a numeric key is searched for in its own forms")

	// The guard that matters: the pattern for a short key must not match a
	// longer one built on it.
	assert.Regexp(t, commitref.GrepPattern("SC-57"), "[SC-57] Add validation")
	assert.NotRegexp(t, commitref.GrepPattern("SC-5"), "[SC-57] Add validation")
	assert.NotRegexp(t, commitref.GrepPattern("42"), "Issue #421")
}

func TestTrimBrackets(t *testing.T) {
	assert.Equal(t, "SC-57", commitref.TrimBrackets(" [SC-57] "))
	assert.Equal(t, "SC-57", commitref.TrimBrackets("SC-57"))
}

func TestIsNumericKey(t *testing.T) {
	assert.True(t, commitref.IsNumericKey("123"))
	assert.False(t, commitref.IsNumericKey("SC-123"))
	assert.False(t, commitref.IsNumericKey(""))
}
