package claude

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	stateWriteKeyPattern = regexp.MustCompile(`human state (?:set|incr) +<[A-Z_]+> +([A-Za-z0-9<>._-]+)`)
	stateReadKeyPattern  = regexp.MustCompile(`human state get +<[A-Z_]+> +([A-Za-z0-9<>._-]+)`)
	// Only `list --prefix` is a read. `rm --prefix` deletes, and counting it as a
	// read is how a counter that is incremented but never compared slips through.
	statePrefixPattern = regexp.MustCompile(`human state list +<[A-Z_]+> +--prefix +([A-Za-z0-9._-]+)`)
)

// writeOnlyStateKeys are the keys a prompt writes deliberately without any
// prompt reading them back. Each needs a reason, because the default assumption
// must be the opposite: a key nobody reads is dead wiring, and the pipeline has
// already shipped three of those.
var writeOnlyStateKeys = map[string]string{
	"budget.fix.flakes":            "diagnostic — surfaced in the run summary for a human, never branched on",
	"budget.implementation.flakes": "diagnostic — same",
	"budget.<stage>.flakes":        "diagnostic — the templated form of the same key",
	"budget.planning.flakes":       "diagnostic — planning stage's flake count, for the run summary",
	// stage.<name> records are read by the DAEMON's retry policy (cmd/cmddaemon
	// stageExitClass), not by another prompt, so a prompt-only scan cannot see
	// the reader. The stage-contract test covers the ones a prompt DOES read
	// back; these are the stages whose only reader is the board.
	"stage.planning":  "read by the daemon's stage-retry policy, not by a prompt",
	"stage.opinion":   "read by the orchestrator prompt AND the daemon; the read is in human-autofix-skill.md",
	"stage.pr-review": "read by the daemon's PR review→fix deploy loop (SC-1387), not by a prompt",
	"stage.pr-fix":    "read by the daemon's PR review→fix deploy loop (SC-1387), not by a prompt",
}

// collectStateKeys returns the concrete keys prompts write and read.
// Templated names (containing "<") are illustrative rather than real keys, so
// they are compared as written and otherwise left alone.
func collectStateKeys(t *testing.T) (writes, reads, prefixes map[string]string) {
	t.Helper()
	writes, reads, prefixes = map[string]string{}, map[string]string{}, map[string]string{}

	entries, err := os.ReadDir("embed")
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body := readEmbed(t, e.Name())
		for _, m := range stateWriteKeyPattern.FindAllStringSubmatch(body, -1) {
			writes[m[1]] = e.Name()
		}
		for _, m := range stateReadKeyPattern.FindAllStringSubmatch(body, -1) {
			reads[m[1]] = e.Name()
		}
		for _, m := range statePrefixPattern.FindAllStringSubmatch(body, -1) {
			prefixes[m[1]] = e.Name()
		}
	}
	return writes, reads, prefixes
}

// Every state key a prompt writes must be read by some prompt.
//
// This is the general form of two defects that shipped: `decisions` was written
// by preflight and read by nobody, so "a retry never re-asks" was not true; and
// a retry counter was incremented without ever being compared against its
// bound, so the bound did nothing. Both look correct in isolation and are only
// visible when the two halves are checked together.
func TestStateKeys_EveryWrittenKeyIsRead(t *testing.T) {
	writes, reads, prefixes := collectStateKeys(t)
	require.NotEmpty(t, writes, "no state writes found — the regex has drifted from the prompts")

	for key, writer := range writes {
		if strings.Contains(key, "<name>") {
			continue // the generic example in a command reference, not a key
		}
		if reason, allowed := writeOnlyStateKeys[key]; allowed {
			require.NotEmpty(t, reason)
			continue
		}
		if _, read := reads[key]; read {
			continue
		}
		// A prefix read (`--prefix budget.`) covers every key beneath it.
		covered := false
		for prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				covered = true
				break
			}
		}
		require.True(t, covered,
			"%s writes state key %q that no prompt ever reads — wire a reader, or record it in writeOnlyStateKeys with a reason",
			writer, key)
	}
}

// The mirror: reading a key nobody writes yields an empty string, and an
// orchestrator that branches on it routes the run on nothing.
func TestStateKeys_EveryReadKeyIsWritten(t *testing.T) {
	writes, reads, _ := collectStateKeys(t)

	for key, reader := range reads {
		if strings.Contains(key, "<") {
			continue // templated illustration
		}
		_, written := writes[key]
		require.True(t, written,
			"%s reads state key %q that no prompt ever writes — it will always be empty", reader, key)
	}
}
