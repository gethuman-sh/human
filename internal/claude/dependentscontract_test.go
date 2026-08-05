package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The pipeline already instructed seven prompts to enumerate callers, and three
// regressions shipped anyway: each dependent that broke had no symbol and no
// call edge — a stored format read by position, a member of a closed vocabulary,
// a convention duplicated in a sibling prompt. The fix is one taxonomy that
// scopes the query to the KIND of shared thing and travels from planning into
// implementation and review. These tests pin that it is present, phrased once,
// and not quietly narrowed back to callers.

// dependentsScopedPrompts are the prompts that change code, and so must carry
// the taxonomy.
var dependentsScopedPrompts = []string{
	"human-planner-agent.md",
	"plan-verify-code-agent.md",
	"human-plan-skill.md",
	"human-executor-agent.md",
	"human-reviewer-agent.md",
	"human-bug-fixer-agent.md",
	"human-pr-fixer-agent.md",
	"human-pr-reviewer-agent.md",
	"human-deploy-fixer-agent.md",
	"human-autofix-skill.md",
	"human-security-fix-skill.md",
	"human-sprint-skill.md",
}

// dependentKinds are the four classifications; a prompt that knows only the
// first is the symbol-only check this ticket replaced.
var dependentKinds = []string{
	"function/type",
	"closed set of values",
	"stored format",
	"instruction/convention",
}

func dependentsFragmentBody(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("embed", "shared", "dependents.md"))
	require.NoError(t, err)
	return string(body)
}

func TestDependents_EveryCodeChangingPromptCarriesTheTaxonomy(t *testing.T) {
	for _, name := range dependentsScopedPrompts {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("embed", name))
			require.NoError(t, err)
			require.Contains(t, string(raw), "<!-- human:include dependents -->",
				"%s changes code, so it must carry the shared dependents taxonomy by include — never a local paraphrase", name)

			content := expanded(t, raw)
			for _, kind := range dependentKinds {
				require.Contains(t, content, kind,
					"%s must name the %q dependent kind", name, kind)
			}
			require.Contains(t, content, "--depth 2",
				"%s must bound the impact query; unbounded it returns hundreds of callers and gets ignored", name)
		})
	}
}

// impact --diff is forced to run locally against an index a pipeline container
// does not have. A prompt that mandates it fails on every board run.
func TestDependents_NeverMandatesTheLocalOnlyDiffForm(t *testing.T) {
	body := dependentsFragmentBody(t)
	require.Contains(t, body, "Never use `human codenav impact --diff`",
		"the fragment must forbid the form that cannot work in a pipeline container")
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "--diff") {
			continue
		}
		require.Contains(t, strings.ToLower(line), "never",
			"every mention of --diff must be the prohibition, not an instruction: %q", line)
	}
}

// Exit 0 with "no changed/seed symbols found" means the seed did not resolve,
// not that nothing depends on the symbol. Reading it as "none" is the silence
// this ticket is about.
func TestDependents_FragmentWarnsThatAnUnresolvedSeedIsNotAnEmptyResult(t *testing.T) {
	body := dependentsFragmentBody(t)
	require.Contains(t, body, "no changed/seed symbols found")
	require.Contains(t, body, "unchecked")
}

// The plan is where the list must live — not in the agent's head.
func TestDependents_PlanFormatCarriesADependentsSection(t *testing.T) {
	planner := readEmbed(t, "human-planner-agent.md")
	require.Contains(t, planner, "## Dependents",
		"the plan output format must carry a Dependents section")
	require.NotContains(t, planner, "Integration points (which functions call this",
		"the weak caller-only variant must be UPGRADED in place, not left beside the new check")
}

// A symbol-only enumeration for a change that is not symbol-only must fail,
// rather than reporting a clean caller count.
func TestDependents_VerifierFailsASymbolOnlyEnumeration(t *testing.T) {
	verifier := readEmbed(t, "plan-verify-code-agent.md")
	require.Contains(t, verifier, "Dependents check:",
		"the verifier must emit a dependents verdict the plan skill can gate on")
	require.Contains(t, verifier, "Unaccounted dependents:",
		"the summary must count dependents, not callers")
	require.NotContains(t, verifier, "Unaccounted callers",
		"the caller-only counter is what let a symbol-only plan read clean; it must be gone")
	require.NotContains(t, verifier, "Check callers and dependents",
		"the weak variant must be upgraded in place, not duplicated")

	// Both consumers of the verifier's report must gate on the same verdict —
	// one acting on it and one ignoring it is the writer/reader drift this
	// whole change exists to close.
	for _, name := range []string{"human-plan-skill.md", "human-sprint-skill.md"} {
		skill := readEmbed(t, name)
		require.Contains(t, skill, "Dependents check:",
			"%s consumes the plan-verify-code report, so it must gate on the verdict", name)
		require.NotContains(t, skill, "unaccounted callers",
			"%s must loop back on the dependents verdict, not on a phrase the report no longer prints", name)
	}
}

// A list nobody acts on is decoration: every implementing prompt must force a
// disposition per dependent.
func TestDependents_ImplementersStateADispositionPerDependent(t *testing.T) {
	for _, name := range []string{
		"human-executor-agent.md",
		"human-bug-fixer-agent.md",
		"human-pr-fixer-agent.md",
		"human-deploy-fixer-agent.md",
	} {
		t.Run(name, func(t *testing.T) {
			content := expanded(t, []byte(readEmbed(t, name)))
			require.Contains(t, content, "examined-and-unchanged")
			require.Contains(t, content, "examined-and-changed")
		})
	}
}

func TestDependents_ReviewerTreatsAnUnexaminedDependentAsIncomplete(t *testing.T) {
	reviewer := readEmbed(t, "human-reviewer-agent.md")
	require.Contains(t, reviewer, "### Unexamined dependents",
		"the reviewer needs a Findings subsection for a dependent nobody looked at")
	require.Contains(t, reviewer, "unexamined dependent",
		"the reviewer must say what an unexamined dependent means for the verdict")
	require.Contains(t, reviewer, "incomplete",
		"an unexamined dependent is an unfinished change, not a passing review")
}

// Silence must not read as "none": every stage record in the scoped set can say
// what it could not determine.
func TestDependents_StageRecordsCanSayUnchecked(t *testing.T) {
	for _, name := range []string{
		"human-plan-skill.md",
		"human-reviewer-agent.md",
		"human-pr-fixer-agent.md",
		"human-deploy-fixer-agent.md",
		"human-autofix-skill.md",
		"human-security-fix-skill.md",
	} {
		t.Run(name, func(t *testing.T) {
			require.Contains(t, readEmbed(t, name), `"unchecked"`,
				"%s writes a stage record, so it must be able to record a dependent kind it could not determine", name)
		})
	}
}

// Guards the include itself: an unknown fragment name fails the install, and a
// silently empty one would remove the rule without failing anything.
func TestDependents_FragmentIsRegisteredAndExpands(t *testing.T) {
	out, err := expandIncludes([]byte("<!-- human:include dependents -->\n"))
	require.NoError(t, err)
	require.Contains(t, string(out), "Dependents: what else depends on the thing you are changing")
	require.NotContains(t, string(out), "human:include")
}

// The fragment ships to every user's project, so it must carry no key from this
// project's own trackers (the same rule install_test.go enforces corpus-wide).
func TestDependents_FragmentCitesNoTrackerKeys(t *testing.T) {
	require.NotRegexp(t, regexp.MustCompile(`\b(SC|HUM)-\d+\b`), dependentsFragmentBody(t))
}
