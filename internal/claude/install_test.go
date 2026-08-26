package claude

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

// expanded is what Install actually writes for an embedded prompt: the source
// with its shared fragments substituted. Comparing against the raw bytes would
// assert an invariant the include mechanism deliberately broke.
func expanded(t *testing.T, content []byte) string {
	t.Helper()
	out, err := expandIncludes(content)
	if err != nil {
		t.Fatalf("expanding fragments: %v", err)
	}
	return string(out)
}

// trackerKeyPattern matches human's own tracker key formats (Shortcut SC-* and
// Linear HUM-*). These are unrelated to whatever tracker a user's project
// runs, so a prompt that cites one ships confusing, unresolvable rationale
// into every installed project and collides with a user's own SC- keys.
var trackerKeyPattern = regexp.MustCompile(`\b(SC|HUM)-\d+\b`)

// TestEmbeddedPromptsCarryNoTrackerKeys locks the fix for the bug where
// prompts under embed/ cited human's own tracker keys (e.g. "SC-782",
// "HUM-59") as rationale or as illustrative examples. Install ships each
// prompt verbatim after expandIncludes, so those keys landed in every user's
// ./.claude, where "SC-" collides 1:1 with the user's own Shortcut keys and
// is unresolvable rationale at best. Every prompt body — including shared
// fragments substituted via expandIncludes — must be free of both formats;
// illustrative examples must use the corpus's existing placeholder tokens
// (<PM_KEY>, <ENG_KEY>, <TICKET_KEY>, <PROJECT_KEY>) instead.
func TestEmbeddedPromptsCarryNoTrackerKeys(t *testing.T) {
	err := filepath.WalkDir("embed", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		content := expanded(t, body)
		if m := trackerKeyPattern.FindString(content); m != "" {
			t.Errorf("%s: shipped prompt body contains tracker key %q; use a placeholder like <TICKET_KEY> instead", path, m)
		}
		return nil
	})
	require.NoError(t, err)
}

// everyProjectGateFixers are the shipped agents/skills that run on ANY card —
// not only in this repo — so each must express its test/lint gate as intent +
// detection, never as this repo's bare Makefile targets. SC-1793 fixed the two
// bug agents this way; SC-2328 extends it to the fixer/verify/gardening agents.
// The two bug agents stay in the list as passing anchors that guard the idiom.
var everyProjectGateFixers = []string{
	"human-pr-fixer-agent.md",
	"human-deploy-fixer-agent.md",
	"human-gardening-skill.md",
	"gardening-triage-agent.md",
	"human-security-verify-agent.md",
	"human-bug-fixer-agent.md",
	"human-bug-verify-agent.md",
}

// makeGatePattern matches a bare invocation of this repo's own Makefile gate.
var makeGatePattern = regexp.MustCompile(`make (test|lint|check)`)

// ecosystemRunnerTokens are the non-Make runners a detect-first instruction
// names so the same gate is followable on a Node/Go/Python/Rust project.
var ecosystemRunnerTokens = []string{"npm test", "go test", "pytest", "cargo test"}

// TestEveryProjectGateFixersDetectTheirGate locks the SC-2328 fix: an agent that
// runs on every card must not name this repo's `make test`/`make lint`/`make
// check` as if universal. Whenever such a prompt still references a make gate
// (kept legitimately as the example of the lean-vs-heavy split), it must also
// carry the detect-first idiom — the word "detect" plus at least one
// per-ecosystem runner — so a project without a Makefile gets a followable
// instruction and a stated fallback instead of a command it cannot run.
func TestEveryProjectGateFixersDetectTheirGate(t *testing.T) {
	for _, name := range everyProjectGateFixers {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("embed", name))
			require.NoError(t, err)
			content := expanded(t, body)
			if !makeGatePattern.MatchString(content) {
				return // no make gate named at all — nothing to qualify
			}
			if !strings.Contains(strings.ToLower(content), "detect") {
				t.Errorf("%s: names a `make` gate but never tells the agent to DETECT the project's runner; express intent + detection the way human-done/human-bug-fixer do", name)
			}
			hasEcosystem := false
			for _, tok := range ecosystemRunnerTokens {
				if strings.Contains(content, tok) {
					hasEcosystem = true
					break
				}
			}
			if !hasEcosystem {
				t.Errorf("%s: names a `make` gate but lists no per-ecosystem runner (%v) as a fallback; a non-Makefile project has nothing to run", name, ecosystemRunnerTokens)
			}
		})
	}
}

type mockFileWriter struct {
	files   map[string][]byte
	dirs    map[string]bool
	mkdirFn func(path string) error
	writeFn func(name string) error
	readFn  func(name string) ([]byte, error)
}

func newMockFileWriter() *mockFileWriter {
	return &mockFileWriter{
		files: make(map[string][]byte),
		dirs:  make(map[string]bool),
	}
}

func (m *mockFileWriter) MkdirAll(path string, _ os.FileMode) error {
	if m.mkdirFn != nil {
		if err := m.mkdirFn(path); err != nil {
			return err
		}
	}
	m.dirs[path] = true
	return nil
}

func (m *mockFileWriter) WriteFile(name string, data []byte, _ os.FileMode) error {
	if m.writeFn != nil {
		if err := m.writeFn(name); err != nil {
			return err
		}
	}
	m.files[name] = data
	return nil
}

func (m *mockFileWriter) ReadFile(name string) ([]byte, error) {
	if m.readFn != nil {
		return m.readFn(name)
	}
	data, ok := m.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

// TestReviewPathPromptsDocumentUnreviewableEscape locks the contract that every
// review-path prompt gives "could not obtain the code" an honest, retryable
// channel: the reviewer agent must name the `unreviewable` outcome, and every
// calling skill must translate it into the daemon's `[human:review-failed]`
// stage-failure marker rather than a `verdict: fail`. Without this, a review
// that never saw the code badges "review found problems" and dispatches rework
// against phantom findings (ticket 653).
func TestReviewPathPromptsDocumentUnreviewableEscape(t *testing.T) {
	// Skills must both recognise the reviewer's unreviewable outcome and route
	// it to the review-failed stage-failure marker.
	skills := []string{
		"human-review-skill.md",
		"human-pickup-review-skill.md",
		"human-autofix-skill.md",
		"human-sprint-skill.md",
	}
	for _, name := range skills {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("embed", name))
			require.NoError(t, err)
			content := string(body)
			assert.Contains(t, content, "unreviewable",
				"%s must recognise the reviewer's unreviewable outcome", name)
			assert.Contains(t, content, "[human:review-failed]",
				"%s must route unreviewable to the review-failed stage-failure marker", name)
		})
	}

	// The reviewer agent must offer `unreviewable` as a distinct outcome (never
	// collapse an unreachable branch or zero-commit case into a fail verdict).
	t.Run("human-reviewer-agent.md", func(t *testing.T) {
		body, err := os.ReadFile(filepath.Join("embed", "human-reviewer-agent.md"))
		require.NoError(t, err)
		assert.Contains(t, string(body), "unreviewable",
			"reviewer agent must offer unreviewable as a distinct outcome")
	})
}

// A stage that could not reach the tracker must not report what it failed to
// read as an absence. The executor's plan lookup ends in "no plan exists", which
// invites re-planning a ticket a human already planned — over a credential store
// that was briefly unavailable. `human plan show` reports the two differently,
// so the prompt must say which is which, and that the unreachable case ends the
// run retryable with nothing changed.
func TestExecutorSeparatesAnUnreadablePlanFromAMissingOne(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("embed", "human-executor-agent.md"))
	require.NoError(t, err)
	content := string(body)

	assert.Contains(t, content, "not \"there is no plan.\"",
		"the executor must be told the two failures are different")
	assert.Contains(t, content, "no [human:plan] comment on ticket",
		"the executor must be given the signal that distinguishes them")
	assert.Contains(t, content, "retryable",
		"an unreachable tracker must end the run retryable, not as a missing plan")
}

// A run that ends must leave a readable account on the ticket. The fix pipeline
// learned this and posts a fix-summary at every terminal point; the feature
// pipeline's implementation stage summarized only in its final message, which in
// board context is read by nobody — so a shipped feature left the card carrying
// branch and commit lines and no prose at all.
func TestEveryFinishingPipelinePostsARunSummary(t *testing.T) {
	for _, name := range []string{
		"human-autofix-skill.md",
		"human-security-fix-skill.md",
		"human-executor-agent.md",
	} {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("embed", name))
			require.NoError(t, err)
			assert.Contains(t, string(body), "fix-summary",
				"%s must leave a plain-language account of the run on the ticket", name)
		})
	}
}

// TestDoneGatePromptsClassifyRedSuites locks the SC-1135 guarantee that the
// done-gate prompts no longer treat any red suite as an automatic NOT DONE.
// A correct fix was failed because an unrelated, pre-existing real-git test
// went red inside the agent's dirty shared checkout; the prompts must instead
// re-classify a red suite in a clean isolated worktree and surface an unrelated,
// pre-existing failure as a non-blocking flag rather than a blocking verdict.
func TestDoneGatePromptsClassifyRedSuites(t *testing.T) {
	t.Run("human-bug-verify-agent.md", func(t *testing.T) {
		body, err := os.ReadFile(filepath.Join("embed", "human-bug-verify-agent.md"))
		require.NoError(t, err)
		content := string(body)
		assert.Contains(t, content, "git worktree add --detach",
			"bug-verify must classify a red suite in a clean isolated worktree")
		assert.Contains(t, content, "non-blocking flag",
			"bug-verify must surface an unrelated pre-existing failure as a non-blocking flag")
		assert.Contains(t, content, "clean",
			"bug-verify must run the baseline classification on a clean checkout")
		assert.NotContains(t, content, "If tests fail, it is NOT DONE. No exceptions.",
			"bug-verify must no longer treat any red suite as an unconditional NOT DONE")
	})

	t.Run("human-done-agent.md", func(t *testing.T) {
		body, err := os.ReadFile(filepath.Join("embed", "human-done-agent.md"))
		require.NoError(t, err)
		content := string(body)
		assert.Contains(t, content, "git worktree add --detach",
			"done agent must classify a red suite in a clean isolated worktree")
		assert.Contains(t, content, "non-blocking flag",
			"done agent must surface an unrelated pre-existing failure as a non-blocking flag")
		assert.NotContains(t, content, "If tests fail, the ticket is not done. No exceptions.",
			"done agent must no longer treat any red suite as an unconditional NOT DONE")
	})
}

func TestInstall_CreatesNewFiles(t *testing.T) {
	fw := newMockFileWriter()
	var buf bytes.Buffer

	err := Install(&buf, fw, false)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Created")

	skillPath := filepath.Join(".claude", "skills", "human-plan", "SKILL.md")
	agentPath := filepath.Join(".claude", "agents", "human-planner.md")
	planVerifyCodeAgentPath := filepath.Join(".claude", "agents", "plan-verify-code.md")
	planVerifyDocsAgentPath := filepath.Join(".claude", "agents", "plan-verify-docs.md")
	readySkillPath := filepath.Join(".claude", "skills", "human-ready", "SKILL.md")
	readyAgentPath := filepath.Join(".claude", "agents", "human-ready.md")
	bugPlanSkillPath := filepath.Join(".claude", "skills", "human-bug-plan", "SKILL.md")
	bugAnalyzerAgentPath := filepath.Join(".claude", "agents", "human-bug-analyzer.md")
	reviewSkillPath := filepath.Join(".claude", "skills", "human-review", "SKILL.md")
	reviewerAgentPath := filepath.Join(".claude", "agents", "human-reviewer.md")
	doneAgentPath := filepath.Join(".claude", "agents", "human-done.md")
	executeSkillPath := filepath.Join(".claude", "skills", "human-execute", "SKILL.md")
	executorAgentPath := filepath.Join(".claude", "agents", "human-executor.md")
	findbugsSkillPath := filepath.Join(".claude", "skills", "human-findbugs", "SKILL.md")
	relateSkillPath := filepath.Join(".claude", "skills", "human-relate", "SKILL.md")
	ideaDraftSkillPath := filepath.Join(".claude", "skills", "human-idea-draft", "SKILL.md")
	findbugsReconAgentPath := filepath.Join(".claude", "agents", "findbugs-recon.md")
	findbugsLogicAgentPath := filepath.Join(".claude", "agents", "findbugs-logic.md")
	findbugsErrorsAgentPath := filepath.Join(".claude", "agents", "findbugs-errors.md")
	findbugsConcurrencyAgentPath := filepath.Join(".claude", "agents", "findbugs-concurrency.md")
	findbugsAPIAgentPath := filepath.Join(".claude", "agents", "findbugs-api.md")
	findbugsTriageAgentPath := filepath.Join(".claude", "agents", "findbugs-triage.md")
	securitySkillPath := filepath.Join(".claude", "skills", "human-security", "SKILL.md")
	securitySurfaceAgentPath := filepath.Join(".claude", "agents", "security-surface.md")
	securityInjectionAgentPath := filepath.Join(".claude", "agents", "security-injection.md")
	securityAuthAgentPath := filepath.Join(".claude", "agents", "security-auth.md")
	securitySecretsAgentPath := filepath.Join(".claude", "agents", "security-secrets.md")
	securityDepsAgentPath := filepath.Join(".claude", "agents", "security-deps.md")
	securityInfraAgentPath := filepath.Join(".claude", "agents", "security-infra.md")
	securitySSRFAgentPath := filepath.Join(".claude", "agents", "security-ssrf.md")
	securityDeserializationAgentPath := filepath.Join(".claude", "agents", "security-deserialization.md")
	securityChainsAgentPath := filepath.Join(".claude", "agents", "security-chains.md")
	securityTriageAgentPath := filepath.Join(".claude", "agents", "security-triage.md")
	brainstormSkillPath := filepath.Join(".claude", "skills", "human-brainstorm", "SKILL.md")
	brainstormReconAgentPath := filepath.Join(".claude", "agents", "brainstorm-recon.md")
	brainstormCodebaseAgentPath := filepath.Join(".claude", "agents", "brainstorm-codebase.md")
	brainstormTrajectoryAgentPath := filepath.Join(".claude", "agents", "brainstorm-trajectory.md")
	brainstormOpportunitiesAgentPath := filepath.Join(".claude", "agents", "brainstorm-opportunities.md")
	brainstormTriageAgentPath := filepath.Join(".claude", "agents", "brainstorm-triage.md")
	ideateSkillPath := filepath.Join(".claude", "skills", "human-ideate", "SKILL.md")
	ideatorAgentPath := filepath.Join(".claude", "agents", "human-ideator.md")
	sprintSkillPath := filepath.Join(".claude", "skills", "human-sprint", "SKILL.md")
	gardeningSkillPath := filepath.Join(".claude", "skills", "human-gardening", "SKILL.md")
	gardeningSurveyAgentPath := filepath.Join(".claude", "agents", "gardening-survey.md")
	gardeningStructureAgentPath := filepath.Join(".claude", "agents", "gardening-structure.md")
	gardeningDuplicationAgentPath := filepath.Join(".claude", "agents", "gardening-duplication.md")
	gardeningComplexityAgentPath := filepath.Join(".claude", "agents", "gardening-complexity.md")
	gardeningHygieneAgentPath := filepath.Join(".claude", "agents", "gardening-hygiene.md")
	gardeningTriageAgentPath := filepath.Join(".claude", "agents", "gardening-triage.md")
	autofixSkillPath := filepath.Join(".claude", "skills", "human-autofix", "SKILL.md")
	bugTriageAgentPath := filepath.Join(".claude", "agents", "human-bug-triage.md")
	bugFixerAgentPath := filepath.Join(".claude", "agents", "human-bug-fixer.md")
	bugVerifyAgentPath := filepath.Join(".claude", "agents", "human-bug-verify.md")
	featuresSkillPath := filepath.Join(".claude", "skills", "human-features", "SKILL.md")
	featuresReconAgentPath := filepath.Join(".claude", "agents", "features-recon.md")
	featuresSynthesisAgentPath := filepath.Join(".claude", "agents", "features-synthesis.md")
	mockupsSkillPath := filepath.Join(".claude", "skills", "human-mockups", "SKILL.md")

	assert.Equal(t, expanded(t, skillContent), string(fw.files[skillPath]))
	assert.Equal(t, expanded(t, agentContent), string(fw.files[agentPath]))
	assert.Equal(t, expanded(t, planVerifyCodeAgentContent), string(fw.files[planVerifyCodeAgentPath]))
	assert.Equal(t, expanded(t, planVerifyDocsAgentContent), string(fw.files[planVerifyDocsAgentPath]))
	assert.Equal(t, expanded(t, readySkillContent), string(fw.files[readySkillPath]))
	assert.Equal(t, expanded(t, readyAgentContent), string(fw.files[readyAgentPath]))
	assert.Equal(t, expanded(t, bugPlanSkillContent), string(fw.files[bugPlanSkillPath]))
	assert.Equal(t, expanded(t, bugAnalyzerAgentContent), string(fw.files[bugAnalyzerAgentPath]))
	assert.Equal(t, expanded(t, reviewSkillContent), string(fw.files[reviewSkillPath]))
	assert.Equal(t, expanded(t, reviewerAgentContent), string(fw.files[reviewerAgentPath]))
	assert.Equal(t, expanded(t, doneAgentContent), string(fw.files[doneAgentPath]))
	assert.Equal(t, expanded(t, executeSkillContent), string(fw.files[executeSkillPath]))
	assert.Equal(t, expanded(t, executorAgentContent), string(fw.files[executorAgentPath]))
	assert.Equal(t, expanded(t, findbugsSkillContent), string(fw.files[findbugsSkillPath]))
	assert.Equal(t, expanded(t, relateSkillContent), string(fw.files[relateSkillPath]))
	assert.Equal(t, expanded(t, ideaDraftSkillContent), string(fw.files[ideaDraftSkillPath]))
	assert.Equal(t, expanded(t, findbugsReconAgentContent), string(fw.files[findbugsReconAgentPath]))
	assert.Equal(t, expanded(t, findbugsLogicAgentContent), string(fw.files[findbugsLogicAgentPath]))
	assert.Equal(t, expanded(t, findbugsErrorsAgentContent), string(fw.files[findbugsErrorsAgentPath]))
	assert.Equal(t, expanded(t, findbugsConcurrencyAgentContent), string(fw.files[findbugsConcurrencyAgentPath]))
	assert.Equal(t, expanded(t, findbugsAPIAgentContent), string(fw.files[findbugsAPIAgentPath]))
	assert.Equal(t, expanded(t, findbugsTriageAgentContent), string(fw.files[findbugsTriageAgentPath]))
	assert.Equal(t, expanded(t, securitySkillContent), string(fw.files[securitySkillPath]))
	assert.Equal(t, expanded(t, securitySurfaceAgentContent), string(fw.files[securitySurfaceAgentPath]))
	assert.Equal(t, expanded(t, securityInjectionAgentContent), string(fw.files[securityInjectionAgentPath]))
	assert.Equal(t, expanded(t, securityAuthAgentContent), string(fw.files[securityAuthAgentPath]))
	assert.Equal(t, expanded(t, securitySecretsAgentContent), string(fw.files[securitySecretsAgentPath]))
	assert.Equal(t, expanded(t, securityDepsAgentContent), string(fw.files[securityDepsAgentPath]))
	assert.Equal(t, expanded(t, securityInfraAgentContent), string(fw.files[securityInfraAgentPath]))
	assert.Equal(t, expanded(t, securitySSRFAgentContent), string(fw.files[securitySSRFAgentPath]))
	assert.Equal(t, expanded(t, securityDeserializationAgentContent), string(fw.files[securityDeserializationAgentPath]))
	assert.Equal(t, expanded(t, securityChainsAgentContent), string(fw.files[securityChainsAgentPath]))
	assert.Equal(t, expanded(t, securityTriageAgentContent), string(fw.files[securityTriageAgentPath]))
	assert.Equal(t, expanded(t, brainstormSkillContent), string(fw.files[brainstormSkillPath]))
	assert.Equal(t, expanded(t, brainstormReconAgentContent), string(fw.files[brainstormReconAgentPath]))
	assert.Equal(t, expanded(t, brainstormCodebaseAgentContent), string(fw.files[brainstormCodebaseAgentPath]))
	assert.Equal(t, expanded(t, brainstormTrajectoryAgentContent), string(fw.files[brainstormTrajectoryAgentPath]))
	assert.Equal(t, expanded(t, brainstormOpportunitiesAgentContent), string(fw.files[brainstormOpportunitiesAgentPath]))
	assert.Equal(t, expanded(t, brainstormTriageAgentContent), string(fw.files[brainstormTriageAgentPath]))
	assert.Equal(t, expanded(t, ideateSkillContent), string(fw.files[ideateSkillPath]))
	assert.Equal(t, expanded(t, ideatorAgentContent), string(fw.files[ideatorAgentPath]))
	assert.Equal(t, expanded(t, sprintSkillContent), string(fw.files[sprintSkillPath]))
	assert.Equal(t, expanded(t, gardeningSkillContent), string(fw.files[gardeningSkillPath]))
	assert.Equal(t, expanded(t, gardeningSurveyAgentContent), string(fw.files[gardeningSurveyAgentPath]))
	assert.Equal(t, expanded(t, gardeningStructureAgentContent), string(fw.files[gardeningStructureAgentPath]))
	assert.Equal(t, expanded(t, gardeningDuplicationAgentContent), string(fw.files[gardeningDuplicationAgentPath]))
	assert.Equal(t, expanded(t, gardeningComplexityAgentContent), string(fw.files[gardeningComplexityAgentPath]))
	assert.Equal(t, expanded(t, gardeningHygieneAgentContent), string(fw.files[gardeningHygieneAgentPath]))
	assert.Equal(t, expanded(t, gardeningTriageAgentContent), string(fw.files[gardeningTriageAgentPath]))
	assert.Equal(t, expanded(t, autofixSkillContent), string(fw.files[autofixSkillPath]))
	assert.Equal(t, expanded(t, bugTriageAgentContent), string(fw.files[bugTriageAgentPath]))
	assert.Equal(t, expanded(t, bugFixerAgentContent), string(fw.files[bugFixerAgentPath]))
	assert.Equal(t, expanded(t, bugVerifyAgentContent), string(fw.files[bugVerifyAgentPath]))
	assert.Equal(t, expanded(t, featuresSkillContent), string(fw.files[featuresSkillPath]))
	assert.Equal(t, expanded(t, featuresReconAgentContent), string(fw.files[featuresReconAgentPath]))
	assert.Equal(t, expanded(t, featuresSynthesisAgentContent), string(fw.files[featuresSynthesisAgentPath]))
	assert.Equal(t, expanded(t, mockupsSkillContent), string(fw.files[mockupsSkillPath]))
}

// The deploy-fixer agent and its slash-skill (SC-1557) must be installed so the
// daemon's deploy-fix dispatch can launch them.
func TestInstall_WritesDeployFixSkillAndAgent(t *testing.T) {
	fw := newMockFileWriter()
	var buf bytes.Buffer

	err := Install(&buf, fw, false)
	require.NoError(t, err)

	agentPath := filepath.Join(".claude", "agents", "human-deploy-fixer.md")
	skillPath := filepath.Join(".claude", "skills", "human-deploy-fix", "SKILL.md")
	assert.NotEmpty(t, fw.files[agentPath], "the deploy-fixer agent must be installed")
	assert.NotEmpty(t, fw.files[skillPath], "the deploy-fix skill must be installed")
	assert.Equal(t, expanded(t, deployFixerAgentContent), string(fw.files[agentPath]))
	assert.Equal(t, expanded(t, deployFixSkillContent), string(fw.files[skillPath]))
}

func TestInstall_OverwritesIdenticalFiles(t *testing.T) {
	fw := newMockFileWriter()

	// Pre-populate with identical content — should still be overwritten.
	skillPath := filepath.Join(".claude", "skills", "human-plan", "SKILL.md")
	fw.files[skillPath] = skillContent

	var buf bytes.Buffer
	err := Install(&buf, fw, false)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Overwriting "+skillPath)
	assert.NotContains(t, buf.String(), "unchanged")
}

func TestInstall_OverwritesChangedFiles(t *testing.T) {
	fw := newMockFileWriter()

	skillPath := filepath.Join(".claude", "skills", "human-plan", "SKILL.md")
	fw.files[skillPath] = []byte("old content")

	var buf bytes.Buffer
	err := Install(&buf, fw, false)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Overwriting "+skillPath)
	assert.Equal(t, expanded(t, skillContent), string(fw.files[skillPath]))
}

func TestInstall_CreatesParentDirectories(t *testing.T) {
	fw := newMockFileWriter()
	var buf bytes.Buffer

	err := Install(&buf, fw, false)

	require.NoError(t, err)

	skillDir := filepath.Join(".claude", "skills", "human-plan")
	readySkillDir := filepath.Join(".claude", "skills", "human-ready")
	bugPlanSkillDir := filepath.Join(".claude", "skills", "human-bug-plan")
	reviewSkillDir := filepath.Join(".claude", "skills", "human-review")
	executeSkillDir := filepath.Join(".claude", "skills", "human-execute")
	findbugsSkillDir := filepath.Join(".claude", "skills", "human-findbugs")
	securitySkillDir := filepath.Join(".claude", "skills", "human-security")
	brainstormSkillDir := filepath.Join(".claude", "skills", "human-brainstorm")
	ideateSkillDir := filepath.Join(".claude", "skills", "human-ideate")
	sprintSkillDir := filepath.Join(".claude", "skills", "human-sprint")
	gardeningSkillDir := filepath.Join(".claude", "skills", "human-gardening")
	autofixSkillDir := filepath.Join(".claude", "skills", "human-autofix")
	featuresSkillDir := filepath.Join(".claude", "skills", "human-features")
	agentDir := filepath.Join(".claude", "agents")
	assert.True(t, fw.dirs[skillDir], "expected plan skill parent directory to be created")
	assert.True(t, fw.dirs[readySkillDir], "expected ready skill parent directory to be created")
	assert.True(t, fw.dirs[bugPlanSkillDir], "expected bug-plan skill parent directory to be created")
	assert.True(t, fw.dirs[reviewSkillDir], "expected review skill parent directory to be created")
	assert.True(t, fw.dirs[executeSkillDir], "expected execute skill parent directory to be created")
	assert.True(t, fw.dirs[findbugsSkillDir], "expected findbugs skill parent directory to be created")
	assert.True(t, fw.dirs[securitySkillDir], "expected security skill parent directory to be created")
	assert.True(t, fw.dirs[brainstormSkillDir], "expected brainstorm skill parent directory to be created")
	assert.True(t, fw.dirs[ideateSkillDir], "expected ideate skill parent directory to be created")
	assert.True(t, fw.dirs[sprintSkillDir], "expected sprint skill parent directory to be created")
	assert.True(t, fw.dirs[gardeningSkillDir], "expected gardening skill parent directory to be created")
	assert.True(t, fw.dirs[autofixSkillDir], "expected autofix skill parent directory to be created")
	assert.True(t, fw.dirs[featuresSkillDir], "expected features skill parent directory to be created")
	assert.True(t, fw.dirs[agentDir], "expected agent parent directory to be created")
}

func TestInstall_WrapsMkdirError(t *testing.T) {
	fw := newMockFileWriter()
	fw.mkdirFn = func(_ string) error {
		return fmt.Errorf("permission denied")
	}

	var buf bytes.Buffer
	err := Install(&buf, fw, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating directory")

	details := errors.AllDetails(err)
	assert.NotEmpty(t, details["path"])
}

func TestInstall_PersonalMode(t *testing.T) {
	fw := newMockFileWriter()
	var buf bytes.Buffer

	err := Install(&buf, fw, true)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Created")

	// Verify files are written under the home directory, not ".claude".
	// The bot git-identity settings.json is the intentional exception: it is
	// always the project file (relative), even under --personal, because the
	// identity is per-project (SC-2854).
	projectGitIdentity := filepath.Join(".claude", "settings.json")
	for path := range fw.files {
		if path == projectGitIdentity {
			continue
		}
		assert.Contains(t, path, ".claude")
		assert.True(t, filepath.IsAbs(path), "personal mode should use absolute home path, got: %s", path)
	}

	// Verify hooks were installed in personal mode.
	assert.Contains(t, buf.String(), "Installing Claude Code hooks")

	var hasSettings bool
	for path := range fw.files {
		if filepath.Base(path) == "settings.json" {
			hasSettings = true
		}
	}
	assert.True(t, hasSettings, "settings.json should be updated in personal mode")
}

func TestInstall_NonPersonalMode_StillInstallsHooks(t *testing.T) {
	fw := newMockFileWriter()
	var buf bytes.Buffer

	err := Install(&buf, fw, false)

	require.NoError(t, err)
	// Hooks are global (~/.claude/settings.json) so they install in all modes.
	assert.Contains(t, buf.String(), "Installing Claude Code hooks")
}

func TestInstall_WrapsWriteError(t *testing.T) {
	fw := newMockFileWriter()
	fw.writeFn = func(_ string) error {
		return fmt.Errorf("disk full")
	}

	var buf bytes.Buffer
	err := Install(&buf, fw, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing file")

	details := errors.AllDetails(err)
	assert.NotEmpty(t, details["path"])
}

func TestInstall_PersonalMode_HomeDirError(t *testing.T) {
	original := userHomeDir
	t.Cleanup(func() { userHomeDir = original })
	userHomeDir = func() (string, error) {
		return "", fmt.Errorf("no home")
	}

	fw := newMockFileWriter()
	var buf bytes.Buffer

	err := Install(&buf, fw, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving home directory")
}

func TestOSFileWriter_MkdirAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	fw := OSFileWriter{}

	err := fw.MkdirAll(dir, 0o755)

	require.NoError(t, err)
	info, statErr := os.Stat(dir)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir())
}

func TestOSFileWriter_WriteAndReadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.txt")
	fw := OSFileWriter{}
	content := []byte("hello world")

	err := fw.WriteFile(path, content, 0o644)
	require.NoError(t, err)

	got, err := fw.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestOSFileWriter_ReadFile_NotFound(t *testing.T) {
	fw := OSFileWriter{}

	_, err := fw.ReadFile(filepath.Join(t.TempDir(), "nonexistent.txt"))

	require.Error(t, err)
}

func TestInstall_ReadFileError_Propagates(t *testing.T) {
	// A non-ENOENT read error must surface, not be silently treated as
	// "missing file" — otherwise we would overwrite a settings file the
	// user can't currently read.
	fw := newMockFileWriter()
	fw.readFn = func(_ string) ([]byte, error) {
		return nil, fmt.Errorf("permission denied")
	}

	var buf bytes.Buffer
	err := Install(&buf, fw, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading settings.json")
}
