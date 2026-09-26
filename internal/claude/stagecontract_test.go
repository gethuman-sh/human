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
	stageReadPattern  = regexp.MustCompile(`human state get (<[A-Z_]+>) stage\.([a-z]+) --field ([a-z_]+)`)
	stageWritePattern = regexp.MustCompile(`human state set (<[A-Z_]+>|SC-\d+) stage\.([a-z]+)\b`)
	// placeholderPattern catches a key or stage name that was never substituted,
	// e.g. `stage.<stage>` — such a record is written under a literal placeholder
	// and is invisible to every reader looking up the concrete stage.
	placeholderPattern = regexp.MustCompile(`human state set \S+ stage\.<`)
)

// embedMarkdownFiles lists every prompt and shared fragment, as paths relative
// to embed/, because a rule stated once in embed/shared/ reaches every prompt
// that includes it and a scan of the top level alone would miss it.
func embedMarkdownFiles(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, dir := range []string{"", "shared"} {
		entries, err := os.ReadDir(filepath.Join("embed", dir))
		require.NoError(t, err)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			names = append(names, filepath.Join(dir, e.Name()))
		}
	}
	return names
}

// readEmbed loads a prompt from the embed directory beside this package.
func readEmbed(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("embed", name))
	require.NoError(t, err)
	return string(body)
}

// keyAlias maps a prompt's local placeholder to the ticket it denotes. A
// pipeline's PM ticket is its own kind of ticket — the bug ticket in autofix,
// the security ticket in security-fix — so a stage recorded under <PM_KEY>,
// <BUG_KEY> or <SEC_KEY> is the same PM-ticket record read back; anything else
// naming a different key is a real mismatch and breaks the handoff.
func keyAlias(key string) string {
	switch key {
	case "<PM_KEY>", "<BUG_KEY>", "<SEC_KEY>":
		return "<PM/BUG_KEY>"
	default:
		return key
	}
}

// stageWriters maps a stage name to EVERY agent prompt that records it. Two
// pipelines (human-autofix and human-security-fix) now share the stage names
// and each has its own triage/verify writer, so a stage can have more than one
// writer; a reader is satisfied by ANY writer that records its field under a
// matching key.
func stageWriters(t *testing.T) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir("embed")
	require.NoError(t, err)

	writers := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body := readEmbed(t, e.Name())
		for _, m := range stageWritePattern.FindAllStringSubmatch(body, -1) {
			stage := m[2]
			// The shared contract's generic example writes stage.<stage>; only
			// concrete per-stage records count as writers.
			if stage == "" {
				continue
			}
			writers[stage] = append(writers[stage], body)
		}
	}
	return writers
}

func TestStageContract_EveryFieldReadIsAlsoWritten(t *testing.T) {
	// Both orchestrators share the stage contract; each reads its own pipeline's
	// records, so validate both against the aggregated writers.
	skills := []string{"human-autofix-skill.md", "human-security-fix-skill.md"}
	writers := stageWriters(t)

	for _, skillName := range skills {
		skill := readEmbed(t, skillName)
		reads := stageReadPattern.FindAllStringSubmatch(skill, -1)
		require.NotEmpty(t, reads, "%s should read stage records as data", skillName)

		for _, read := range reads {
			readKey, stage, field := read[1], read[2], read[3]

			bodies, ok := writers[stage]
			require.True(t, ok, "%s reads stage.%s but no agent prompt records it", skillName, stage)

			// A reader is satisfied when SOME writer records the field under a
			// key that aliases to the reader's key — a record written under a
			// different ticket key is invisible to the reader (latent while the
			// keys happen to be equal, broken the first time they are not).
			satisfied := false
			for _, body := range bodies {
				if strings.Contains(body, `"`+field+`"`) && keyAlias(stageWriteKey(body, stage)) == keyAlias(readKey) {
					satisfied = true
					break
				}
			}
			require.True(t, satisfied,
				"%s reads stage.%s --field %s under %s, but no agent that writes stage.%s records %q under a matching key",
				skillName, stage, field, readKey, stage, field)
		}
	}
}

var (
	// The type group admits placeholders on purpose: a prompt that tells an
	// agent to post `<stage>-failed` posts a marker the board never classifies,
	// and a pattern that required a letter first skipped exactly that line —
	// which lived in a shared fragment, so the scan below reads embed/shared
	// as well as the prompts that include it (SC-5179).
	markerPostPattern = regexp.MustCompile(`human marker post \S+ ([a-z<][a-zA-Z<>_-]*)`)
	taskModelPattern  = regexp.MustCompile(`Task\(subagent_type="([a-z-]+)", model="([^"]+)"`)
	// anyTaskPattern matches every dispatch, tiered or not. taskModelPattern
	// cannot answer "which agent was dispatched without a model", because a site
	// that names none does not match it at all — the silence AC1 is about.
	anyTaskPattern = regexp.MustCompile(`Task\(subagent_type="([a-z-]+)"(, model="([^"]+)")?`)
	// taskCallSite finds a dispatch site HOWEVER it is written, so a site that
	// does not match the canonical head below is reported rather than skipped —
	// a regexp that only matches well-formed dispatches answers "are the
	// well-formed ones tiered", which is not the question. The \b is what keeps
	// prose out: `URLSession.shared.dataTask(with:)` (security-ssrf-agent.md:21)
	// has no word boundary before `Task(`.
	taskCallSite = regexp.MustCompile(`\bTask\(`)
	// canonicalDispatch is anchored, so it is applied to the text starting AT a
	// site: the dispatch must name subagent_type first and model second. The
	// shape is pinned rather than merely the presence of `model=` anywhere on
	// the line, because a `model="…"` occurring inside a prompt string would
	// otherwise satisfy a completeness check it has nothing to do with.
	canonicalDispatch = regexp.MustCompile(`^Task\(subagent_type="([a-z-]+)", model="([^"]+)"`)
	// The Task tool accepts model aliases, never full model ids. Verified
	// against the Claude Code 2.1.218 input schema:
	//   model: z.enum(["sonnet","opus","haiku","fable"]).optional()
	//
	// The set is read from the model card rather than restated here: it used to
	// be a third hand-written copy of the same vocabulary (the rate card and
	// embed/shared/model-tiers.md held the other two), and three copies of a
	// closed set is three chances to disagree (SC-3580).
	validTaskModels = taskModelAliasSet()
)

func taskModelAliasSet() map[string]bool {
	set := map[string]bool{}
	for _, alias := range taskModelAliases() {
		set[alias] = true
	}
	return set
}

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

// lineAt reports the 1-based line a byte offset falls on, so a failure names a
// place a reader can open rather than an offset.
func lineAt(body string, offset int) int {
	return strings.Count(body[:offset], "\n") + 1
}

// Every dispatch the tool ships names its tier. This is the COMPLETENESS guard
// and it deliberately holds no list of agents: policyTopTier enumerated 14 by
// name, so every agent of a newly added pipeline passed the build untiered —
// 32 of them did, across eight pipelines, until SC-3583. An unnamed dispatch is
// not a cheap tier and not an expensive one; it is whatever the project set as
// its container default (SC-5474), so a security fleet can run a tier below
// policy with no record but an `inherited` row.
func TestPrompts_EveryDispatchNamesATier(t *testing.T) {
	sites := 0
	for _, name := range embedMarkdownFiles(t) {
		body := readEmbed(t, name)
		for _, loc := range taskCallSite.FindAllStringIndex(body, -1) {
			sites++
			m := canonicalDispatch.FindStringSubmatch(body[loc[0]:])
			require.NotNilf(t, m,
				`%s:%d dispatches without naming a tier (or not in the canonical shape). `+
					`Write Task(subagent_type="<agent>", model="<opus|sonnet|haiku|fable>", prompt=…) — `+
					`omitting model does not inherit a tier, it inherits the project's container default`,
				name, lineAt(body, loc[0]))
			require.Truef(t, validTaskModels[m[2]],
				"%s:%d dispatches %s with model=%q; valid values are %v",
				name, lineAt(body, loc[0]), m[1], m[2], taskModelAliases())
		}
	}
	require.Positive(t, sites, "no dispatch sites found — the pattern has drifted from the prompts")
}

// policyTopTier names every agent whose work embed/shared/model-tiers.md puts
// at or above `opus`, with the phrase from the table that puts it there. It is
// written out rather than derived from the agent's name because a pattern would
// sweep in agents that produce no verdict (human-ready, human-ideator) and would
// release one that gets renamed.
//
// It asserts WHICH tier and never whether one was named: completeness is
// TestPrompts_EveryDispatchNamesATier's job, precisely because a list of names
// cannot cover an agent nobody added to it. Centralizing the stage->tier map in
// Go was considered and refused (SC-3583): the shared fragment carries the
// rules and the per-pipeline assignment is where a reader looks for it.
var policyTopTier = map[string]string{
	"human-planner":         "planning",
	"human-reviewer":        "review verdicts",
	"human-pr-reviewer":     "review verdicts (pre-merge adversarial gate)",
	"human-ticket-reviewer": "review verdicts, on a ticket",
	"human-bug-analyzer":    "root-cause analysis",
	"human-bug-triage":      "root-cause analysis",
	"human-security-triage": "root-cause analysis",
	"human-preflight":       "decides what a person must answer — a wrong answer stops nothing and is silent",
	"gardening-triage":      "finding verdicts",
	"brainstorm-triage":     "finding verdicts",
	"findbugs-triage":       "finding verdicts",
	"security-triage":       "finding verdicts",
	"human-second-opinion":  "adversarial challenges",
	"human-verdict-skeptic": "adversarial challenges",
}

// `inherited` is not a tier, it is the absence of a decision: a dispatch that
// names no model runs on whatever the account defaults to, which stays expensive
// by accident today and goes cheap by accident the moment a project sets
// agent.model. Pin that every top-tier agent names its tier, and never below
// opus (SC-5474). Completeness moved to TestPrompts_EveryDispatchNamesATier
// (SC-3583); this pins the tier, not its presence.
func TestPrompts_TopTierAgentsNameTheirTier(t *testing.T) {
	opus := modelRank("opus")
	require.Positive(t, opus, "the model card must rank opus")

	seen := map[string]bool{}
	for _, name := range embedMarkdownFiles(t) {
		for _, m := range anyTaskPattern.FindAllStringSubmatch(readEmbed(t, name), -1) {
			agent, model := m[1], m[3]
			why, top := policyTopTier[agent]
			if !top {
				continue
			}
			seen[agent] = true
			require.GreaterOrEqualf(t, modelRank(model), opus,
				"%s dispatches %s (%s) at %q, below opus", name, agent, why, model)
		}
	}
	for agent := range policyTopTier {
		require.Truef(t, seen[agent], "%s is in the top-tier set but no prompt dispatches it", agent)
	}
}

// An adversarial check that runs on a weaker model gets argued out of its
// objection, which turns the gate into a rubber stamp — worse than no gate,
// because it manufactures confidence. Pin the adversaries at or above the top
// tier — fable is a move up and must not fail this.
//
// Scanning every prompt rather than one skill: the security fix pipeline
// dispatches both adversaries too and was covered by nothing, and a third
// pipeline would start uncovered as well (SC-3583).
func TestPrompts_AdversarialChecksAreNotTieredDown(t *testing.T) {
	adversaries := []string{"human-verdict-skeptic", "human-second-opinion"}
	for _, agent := range adversaries {
		found := false
		for _, name := range embedMarkdownFiles(t) {
			for _, m := range taskModelPattern.FindAllStringSubmatch(readEmbed(t, name), -1) {
				if m[1] != agent {
					continue
				}
				found = true
				require.GreaterOrEqualf(t, modelRank(m[2]), modelRank("opus"),
					"%s dispatches %s at %q; an adversary runs at opus or above", name, agent, m[2])
			}
		}
		require.True(t, found, "%s is never dispatched with an explicit model", agent)
	}
}

// Rule 3 of embed/shared/model-tiers.md: a cheap fan-out is followed by a
// top-tier validator. That validator is the only place a contested cheap answer
// is actually re-asked, so a fleet shipped without one has no escalation — only
// unreviewed output. Two is the threshold because a lone sub-opus dispatch is
// not a fan-out: human-execute, human-pr-fix and human-deploy-fix each have
// exactly one, and so does the policy fragment's own worked example.
func TestPrompts_ACheapFanOutHasATopTierValidator(t *testing.T) {
	opus := modelRank("opus")
	require.Positive(t, opus, "the model card must rank opus")

	for _, name := range embedMarkdownFiles(t) {
		cheap, top := 0, 0
		for _, m := range taskModelPattern.FindAllStringSubmatch(readEmbed(t, name), -1) {
			if modelRank(m[2]) < opus {
				cheap++
				continue
			}
			top++
		}
		if cheap < 2 {
			continue
		}
		require.Positivef(t, top,
			"%s fans out to %d agents below opus and dispatches nothing at opus or above — "+
				"the phase that reads their output is where a contested cheap answer is re-asked",
			name, cheap)
	}
}

// haikuUnusedNote is the load-bearing phrase of the note, kept here so the test
// pins the claim and not the paragraph's wording around it.
const haikuUnusedNote = "`haiku` has no shipped dispatch today"

// The cheapest tier is used by a shipped dispatch, or the policy says out loud
// that nothing shipped has that shape — never neither. The ticket that asked for
// this offered "use it or delete the row" as a binary, and the tempting way to
// satisfy it was to move a recon step to haiku: recon output is the sole input
// to six to ten dependent agents and a wrong one is silent, which is the opus
// row. So the row stays (a project may set agent.model: haiku) and its emptiness
// is recorded as a finding. This is an XOR: the moment a haiku dispatch ships,
// the note is false and must go (SC-3583).
func TestPrompts_TheHaikuRowSaysWhetherAnythingUsesIt(t *testing.T) {
	used := false
	for _, name := range embedMarkdownFiles(t) {
		for _, m := range taskModelPattern.FindAllStringSubmatch(readEmbed(t, name), -1) {
			if m[2] == "haiku" {
				used = true
			}
		}
	}
	noted := strings.Contains(readEmbed(t, filepath.Join("shared", "model-tiers.md")), haikuUnusedNote)
	if used {
		require.False(t, noted,
			"a shipped dispatch now runs at haiku, but model-tiers.md still records the tier as unused — remove the note")
		return
	}
	require.True(t, noted,
		"no shipped dispatch runs at haiku and model-tiers.md does not say so — "+
			"record the absence rather than leaving the row looking used")
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

	posts := 0
	for _, name := range embedMarkdownFiles(t) {
		for _, m := range markerPostPattern.FindAllStringSubmatch(readEmbed(t, name), -1) {
			posts++
			require.True(t, known[m[1]],
				"%s posts [human:%s], which the marker protocol does not define — "+
					"add it to internal/marker specs, or use the existing marker for that job",
				name, m[1])
		}
	}
	require.Positive(t, posts, "no marker posts found — the regex has drifted from the prompts")
}

// The verdict vocabularies are the routing keys: the skill branches on these
// exact words, so the agent that produces them must offer the same set.
func TestStageContract_VerdictVocabulariesMatch(t *testing.T) {
	pipelines := []struct {
		skill string
		cases []struct {
			agent  string
			values []string
		}
	}{
		{
			skill: "human-autofix-skill.md",
			cases: []struct {
				agent  string
				values []string
			}{
				{"human-bug-triage-agent.md", []string{"confirmed", "not-a-bug", "undetermined"}},
				{"human-verdict-skeptic-agent.md", []string{"upheld", "refuted"}},
				{"human-bug-verify-agent.md", []string{"DONE", "NOT DONE"}},
				{"human-reviewer-agent.md", []string{"pass", "pass with notes", "fail", "incomplete", "unreviewable", "decision-required"}},
			},
		},
		{
			// The security pipeline shares the reviewer and skeptic and swaps in
			// its own triage/verify, but branches on the identical vocabularies.
			skill: "human-security-fix-skill.md",
			cases: []struct {
				agent  string
				values []string
			}{
				{"human-security-triage-agent.md", []string{"confirmed", "not-a-bug", "undetermined"}},
				{"human-verdict-skeptic-agent.md", []string{"upheld", "refuted"}},
				{"human-security-verify-agent.md", []string{"DONE", "NOT DONE"}},
				{"human-reviewer-agent.md", []string{"pass", "pass with notes", "fail", "incomplete", "unreviewable", "decision-required"}},
			},
		},
		{
			// The board review path and the out-of-band pickup path dispatch the
			// same reviewer and must handle every value it can produce, including
			// the two pre-verdict escapes (SC-5277).
			skill: "human-review-skill.md",
			cases: []struct {
				agent  string
				values []string
			}{
				{"human-reviewer-agent.md", []string{"pass", "pass with notes", "fail", "incomplete", "unreviewable", "decision-required"}},
			},
		},
		{
			skill: "human-pickup-review-skill.md",
			cases: []struct {
				agent  string
				values []string
			}{
				{"human-reviewer-agent.md", []string{"pass", "pass with notes", "fail", "incomplete", "unreviewable", "decision-required"}},
			},
		},
	}

	for _, p := range pipelines {
		skill := readEmbed(t, p.skill)
		for _, c := range p.cases {
			body := readEmbed(t, c.agent)
			for _, v := range c.values {
				require.Contains(t, body, v, "%s must offer the verdict %q %s branches on", c.agent, v, p.skill)
				require.Contains(t, skill, v, "%s must handle the verdict %q that %s can produce", p.skill, v, c.agent)
			}
		}
	}
}

// stageWriteKey returns the placeholder a prompt records the stage under.
func stageWriteKey(body, stage string) string {
	for _, m := range stageWritePattern.FindAllStringSubmatch(body, -1) {
		if m[2] == stage {
			return m[1]
		}
	}
	return ""
}

// A record written under a literal placeholder is written under a key nobody
// reads. The shared exit contract carries an example, so this also pins that
// the example stays concrete.
func TestPrompts_NoUnsubstitutedStageKeys(t *testing.T) {
	entries, err := os.ReadDir("embed")
	require.NoError(t, err)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body := readEmbed(t, e.Name())
		require.NotRegexp(t, placeholderPattern, body,
			"%s records a stage under a literal placeholder; substitute the concrete stage name", e.Name())
	}
	// The shared fragments are expanded into prompts, so check them too.
	shared, err := os.ReadDir(filepath.Join("embed", "shared"))
	require.NoError(t, err)
	for _, e := range shared {
		body, err := os.ReadFile(filepath.Join("embed", "shared", e.Name()))
		require.NoError(t, err)
		require.NotRegexp(t, placeholderPattern, string(body),
			"shared/%s records a stage under a literal placeholder", e.Name())
	}
}

// The budget-spent stop in the two fix skills is the pipeline's highest-volume
// needs-human-work path; its marker template must carry the four blocker
// fields the shared contract requires (SC-5179).
func TestPrompts_BudgetSpentStopCarriesTheBlockerFields(t *testing.T) {
	for _, name := range []string{"human-autofix-skill.md", "human-security-fix-skill.md"} {
		body := readEmbed(t, name)
		i := strings.Index(body, "implementation-failed \\")
		require.Positive(t, i, "%s: the budget-spent implementation-failed template must exist", name)
		window := body[i:min(len(body), i+900)]
		for _, field := range []string{"--field kind=", "--field evidence=", "--field attempted=", "--field release="} {
			require.Contains(t, window, field, "%s: the implementation-failed template must carry %s", name, field)
		}
		require.Contains(t, body, `"blocker":{"kind":`, "%s: the stage record must carry the blocker object", name)
	}
}

// A no-fix terminal writes its record BEFORE it closes the ticket. The other
// order left a closed ticket with no trace of why whenever the post was refused,
// and the marker — not the closed status — is what the board reads for the
// resolved column and what a person re-opens from (SC-5839). Both fix pipelines
// state the rule, so both are checked: one of them drifting back is exactly the
// failure this pins.
func TestPrompts_TheNoFixRecordIsPostedBeforeTheTicketIsClosed(t *testing.T) {
	for _, name := range []string{"human-autofix-skill.md", "human-security-fix-skill.md"} {
		skill := readEmbed(t, name)

		post := strings.Index(skill, "marker post <BUG_KEY> no-fix-needed --field verdict=not-a-bug")
		if post < 0 {
			post = strings.Index(skill, "marker post <SEC_KEY> no-fix-needed --field verdict=not-a-bug")
		}
		require.Positive(t, post, "%s no longer posts the not-a-bug terminal marker", name)

		closeIdx := strings.Index(skill, "human close <")
		require.Positive(t, closeIdx, "%s no longer closes the ticket on a not-a-bug terminal", name)

		require.Less(t, post, closeIdx,
			"%s tells the run to close the ticket before posting [human:no-fix-needed] — "+
				"a refused record then leaves a closed ticket with no trace (SC-5839)", name)
	}
}
