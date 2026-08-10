package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmdagentcontext"
)

// The fragment is injected into every coding-agent session as instructions, so
// a command named in it that the binary does not have is an instruction the
// agent cannot carry out — and one it cannot detect having failed, because a
// group command answers an unknown subcommand with its own help text. It named
// `human fsm next`, `fsm show` and `fsm states` for as long as it took someone
// to try one by hand.
func TestAgentContext_EveryCommandItNamesResolves(t *testing.T) {
	root := newRootCmd()
	invocations := agentContextInvocations(t)
	require.NotEmpty(t, invocations, "no `human …` invocation was extracted — the parser proves nothing")

	for _, args := range invocations {
		cmd, rest, err := root.Find(args)
		if assert.NoError(t, err, "human %s", strings.Join(args, " ")) {
			assert.Empty(t, rest, "the fragment names `human %s`, but %q resolves no further than `%s`",
				strings.Join(args, " "), strings.Join(rest, " "), cmd.CommandPath())
		}
	}
}

// A count, so deleting the fragment's whole command surface cannot pass as
// "every command it names resolves".
func TestAgentContext_NamesTheToolItIsFor(t *testing.T) {
	invocations := agentContextInvocations(t)
	assert.GreaterOrEqual(t, len(invocations), 20,
		"the fragment stopped naming most of the commands it exists to teach")

	paths := make(map[string]bool, len(invocations))
	for _, args := range invocations {
		paths[strings.Join(args, " ")] = true
	}
	// The four surfaces the fragment is written around; each has its own section
	// and losing one silently is the failure this guards.
	for _, want := range []string{"codenav def", "get", "marker post", "fsm where", "deploy"} {
		assert.True(t, paths[want], "the fragment no longer names `human %s`", want)
	}
}

// agentContextInvocations reads the fragment through the command that ships it,
// not from disk, so the test covers the bytes an agent is actually given.
func agentContextInvocations(t *testing.T) [][]string {
	t.Helper()
	cmd := cmdagentcontext.BuildAgentContextCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs(nil)
	require.NoError(t, cmd.Execute())
	return parseHumanInvocations(buf.String())
}

var backtickSpan = regexp.MustCompile("`([^`]+)`")

// parseHumanInvocations pulls the resolvable subcommand path out of every
// `human …` span in the fragment. It walks words while they are literal
// subcommand names and stops at the first placeholder, flag or argument —
// everything after that is for the reader, not for cobra. A word carrying
// `a|b` alternatives expands into one invocation per alternative.
func parseHumanInvocations(text string) [][]string {
	var out [][]string
	for _, m := range backtickSpan.FindAllStringSubmatch(text, -1) {
		words := strings.Fields(m[1])
		if len(words) < 2 || words[0] != "human" {
			continue
		}
		if path := literalPath(words[1:]); len(path) > 0 {
			out = append(out, expandAlternatives(path)...)
		}
	}
	return out
}

// literalPath keeps the leading run of words that name commands.
func literalPath(words []string) [][]string {
	var path [][]string
	for _, w := range words {
		// The fragment teaches the tracker commands generically, and every
		// tracker has the same subcommands under it — so resolve the rest of
		// the path against one real tracker rather than abandoning the line.
		if w == "<tracker>" {
			path = append(path, []string{"jira"})
			continue
		}
		alts := strings.Split(w, "|")
		if !allCommandNames(alts) {
			break
		}
		path = append(path, alts)
	}
	return path
}

var commandName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func allCommandNames(words []string) bool {
	for _, w := range words {
		if !commandName.MatchString(w) {
			return false
		}
	}
	return true
}

// expandAlternatives turns a path of per-position alternatives into one
// invocation per combination.
func expandAlternatives(path [][]string) [][]string {
	out := [][]string{{}}
	for _, alts := range path {
		next := make([][]string, 0, len(out)*len(alts))
		for _, prefix := range out {
			for _, alt := range alts {
				next = append(next, append(append([]string{}, prefix...), alt))
			}
		}
		out = next
	}
	return out
}

// The parser is the load-bearing half of the guard above: if it silently
// stopped extracting anything, every fragment would pass.
func TestParseHumanInvocations(t *testing.T) {
	got := parseHumanInvocations(strings.Join([]string{
		"- `human codenav def <name>` — go-to-definition (`--outline` for names)",
		"- `human marker post|show|list <KEY> [TYPE]` — markers",
		"- `human <tracker> issue create|edit` — tickets",
		"- `human pr create --head <branch> --title \"…\"` — open a PR",
		"- `human codenav index .` — index",
		"- backticked prose with no command at all",
	}, "\n"))

	assert.Equal(t, [][]string{
		{"codenav", "def"},
		{"marker", "post"}, {"marker", "show"}, {"marker", "list"},
		{"jira", "issue", "create"}, {"jira", "issue", "edit"},
		{"pr", "create"},
		{"codenav", "index"},
	}, got)
}

// Placeholders differ only by punctuation from the words around them, so the
// stop rule gets its own case: a path must never absorb one as a subcommand.
func TestParseHumanInvocations_StopsAtPlaceholders(t *testing.T) {
	for _, span := range []string{
		"`human get <KEY>`",
		"`human search \"<query>\"`",
		"`human commits prefix <PM> [<ENG>]`",
		"`human deploy <KEY>`",
	} {
		got := parseHumanInvocations(span)
		require.Len(t, got, 1, span)
		for _, word := range got[0] {
			assert.True(t, commandName.MatchString(word), "%s yielded the placeholder %q", span, word)
		}
	}
}

// Guard the guard: a fragment naming a command that does not exist must fail
// the check above, whatever cobra does with the leftover word.
func TestAgentContextGuard_CatchesAMissingCommand(t *testing.T) {
	root := newRootCmd()
	_, rest, err := root.Find([]string{"fsm", "next"})
	require.NoError(t, err)
	assert.Equal(t, []string{"next"}, rest, "an unknown subcommand must be left over, not swallowed")
}
