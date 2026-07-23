package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
)

// The pipeline's stage handoffs are structured records, not prose: an
// orchestrator reads `human state get <KEY> stage.<s> --field <f>` and the
// stage agent writes that field into `stage.<s>`. The two halves live in
// different prompt files, so nothing but this test stops them from drifting —
// and a read of a field nobody writes fails silently at runtime, routing the
// run on an empty string.

var (
	stageReadPattern  = regexp.MustCompile(`stage\.([a-z]+) --field ([a-z_]+)`)
	stageWritePattern = regexp.MustCompile(`human state set [^\n]*\bstage\.([a-z]+)\b`)
)

// readEmbed loads a prompt from the embed directory beside this package.
func readEmbed(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("embed", name))
	require.NoError(t, err)
	return string(body)
}

// stageWriters maps a stage name to the prompt that records it.
func stageWriters(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir("embed")
	require.NoError(t, err)

	writers := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body := readEmbed(t, e.Name())
		for _, m := range stageWritePattern.FindAllStringSubmatch(body, -1) {
			stage := m[1]
			// The shared contract's generic example writes stage.<stage>; only
			// concrete per-stage records count as writers.
			if stage == "" {
				continue
			}
			writers[stage] = body
		}
	}
	return writers
}

func TestStageContract_EveryFieldReadIsAlsoWritten(t *testing.T) {
	skill := readEmbed(t, "human-autofix-skill.md")
	writers := stageWriters(t)

	reads := stageReadPattern.FindAllStringSubmatch(skill, -1)
	require.NotEmpty(t, reads, "the orchestrator should read stage records as data")

	for _, read := range reads {
		stage, field := read[1], read[2]

		writer, ok := writers[stage]
		require.True(t, ok, "the skill reads stage.%s but no agent prompt records it", stage)
		require.Contains(t, writer, `"`+field+`"`,
			"the skill reads stage.%s --field %s, but the agent that writes stage.%s never records %q",
			stage, field, stage, field)
	}
}

var (
	markerPostPattern = regexp.MustCompile(`human marker post \S+ ([a-z][a-z-]*)`)
	taskModelPattern  = regexp.MustCompile(`Task\(subagent_type="([a-z-]+)", model="([^"]+)"`)
	// The Task tool accepts model aliases, never full model ids. Verified
	// against the Claude Code 2.1.218 input schema:
	//   model: z.enum(["sonnet","opus","haiku","fable"]).optional()
	validTaskModels = map[string]bool{"opus": true, "sonnet": true, "haiku": true, "fable": true}
)

// A model override is only honoured if it is one of the tool's aliases. Writing
// a real model id ("claude-opus-4-8") is the natural mistake and would be
// rejected at dispatch, at which point a tiering decision silently does
// nothing — so pin the vocabulary here.
func TestPrompts_DispatchModelsAreValidAliases(t *testing.T) {
	entries, err := os.ReadDir("embed")
	require.NoError(t, err)

	dispatches := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		for _, m := range taskModelPattern.FindAllStringSubmatch(readEmbed(t, e.Name()), -1) {
			dispatches++
			require.True(t, validTaskModels[m[2]],
				"%s dispatches %s with model=%q; valid values are opus, sonnet, haiku, fable",
				e.Name(), m[1], m[2])
		}
	}
	require.Positive(t, dispatches, "no tiered dispatches found — the pipeline pays for a model it did not choose")
}

// An adversarial check that runs on a weaker model gets argued out of its
// objection, which turns the gate into a rubber stamp — worse than no gate,
// because it manufactures confidence. Pin the adversaries to the top tier.
func TestPrompts_AdversarialChecksAreNotTieredDown(t *testing.T) {
	skill := readEmbed(t, "human-autofix-skill.md")

	adversaries := []string{"human-verdict-skeptic", "human-second-opinion"}
	for _, agent := range adversaries {
		found := false
		for _, m := range taskModelPattern.FindAllStringSubmatch(skill, -1) {
			if m[1] != agent {
				continue
			}
			found = true
			require.Equal(t, "opus", m[2], "%s must run at the top tier", agent)
		}
		require.True(t, found, "%s is never dispatched with an explicit model", agent)
	}
}

// Every marker a prompt posts must be a type the protocol knows.
//
// This guards against the easiest mistake in this pipeline: inventing a new
// marker for a job an existing one already does. The board's decision loop —
// [human:options] rendered as "Decision needed", answered with
// [human:option-chosen], and exempted from the failure watcher by
// stagePausedOnOptions — already parks a card on a human decision. A parallel
// "needs-input" marker would split that trail in half: one path the board
// renders and resumes, another it does not.
func TestPrompts_PostOnlyKnownMarkerTypes(t *testing.T) {
	known := map[string]bool{}
	for _, k := range marker.KnownTypes() {
		known[k] = true
	}

	entries, err := os.ReadDir("embed")
	require.NoError(t, err)

	posts := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		for _, m := range markerPostPattern.FindAllStringSubmatch(readEmbed(t, e.Name()), -1) {
			posts++
			require.True(t, known[m[1]],
				"%s posts [human:%s], which the marker protocol does not define — "+
					"add it to internal/marker specs, or use the existing marker for that job",
				e.Name(), m[1])
		}
	}
	require.Positive(t, posts, "no marker posts found — the regex has drifted from the prompts")
}

// The verdict vocabularies are the routing keys: the skill branches on these
// exact words, so the agent that produces them must offer the same set.
func TestStageContract_VerdictVocabulariesMatch(t *testing.T) {
	cases := []struct {
		agent  string
		values []string
	}{
		{"human-bug-triage-agent.md", []string{"confirmed", "not-a-bug", "undetermined"}},
		{"human-verdict-skeptic-agent.md", []string{"upheld", "refuted"}},
		{"human-bug-verify-agent.md", []string{"DONE", "NOT DONE"}},
		{"human-reviewer-agent.md", []string{"pass", "pass with notes", "fail", "unreviewable"}},
	}
	skill := readEmbed(t, "human-autofix-skill.md")

	for _, c := range cases {
		body := readEmbed(t, c.agent)
		for _, v := range c.values {
			require.Contains(t, body, v, "%s must offer the verdict %q the skill branches on", c.agent, v)
			require.Contains(t, skill, v, "the skill must handle the verdict %q that %s can produce", v, c.agent)
		}
	}
}
