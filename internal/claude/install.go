package claude

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gethuman-sh/human/errors"
)

//go:embed embed/human-plan-skill.md
var skillContent []byte

//go:embed embed/human-planner-agent.md
var agentContent []byte

//go:embed embed/plan-verify-code-agent.md
var planVerifyCodeAgentContent []byte

//go:embed embed/plan-verify-docs-agent.md
var planVerifyDocsAgentContent []byte

//go:embed embed/human-ready-skill.md
var readySkillContent []byte

//go:embed embed/human-ready-agent.md
var readyAgentContent []byte

//go:embed embed/human-bug-plan-skill.md
var bugPlanSkillContent []byte

//go:embed embed/human-bug-analyzer-agent.md
var bugAnalyzerAgentContent []byte

//go:embed embed/human-review-skill.md
var reviewSkillContent []byte

//go:embed embed/human-reviewer-agent.md
var reviewerAgentContent []byte

//go:embed embed/human-done-agent.md
var doneAgentContent []byte

//go:embed embed/human-execute-skill.md
var executeSkillContent []byte

//go:embed embed/human-executor-agent.md
var executorAgentContent []byte

//go:embed embed/human-findbugs-skill.md
var findbugsSkillContent []byte

//go:embed embed/findbugs-recon-agent.md
var findbugsReconAgentContent []byte

//go:embed embed/findbugs-logic-agent.md
var findbugsLogicAgentContent []byte

//go:embed embed/findbugs-errors-agent.md
var findbugsErrorsAgentContent []byte

//go:embed embed/findbugs-concurrency-agent.md
var findbugsConcurrencyAgentContent []byte

//go:embed embed/findbugs-api-agent.md
var findbugsAPIAgentContent []byte

//go:embed embed/findbugs-triage-agent.md
var findbugsTriageAgentContent []byte

//go:embed embed/human-security-skill.md
var securitySkillContent []byte

//go:embed embed/security-surface-agent.md
var securitySurfaceAgentContent []byte

//go:embed embed/security-injection-agent.md
var securityInjectionAgentContent []byte

//go:embed embed/security-auth-agent.md
var securityAuthAgentContent []byte

//go:embed embed/security-secrets-agent.md
var securitySecretsAgentContent []byte

//go:embed embed/security-deps-agent.md
var securityDepsAgentContent []byte

//go:embed embed/security-infra-agent.md
var securityInfraAgentContent []byte

//go:embed embed/security-chains-agent.md
var securityChainsAgentContent []byte

//go:embed embed/security-triage-agent.md
var securityTriageAgentContent []byte

//go:embed embed/human-brainstorm-skill.md
var brainstormSkillContent []byte

//go:embed embed/brainstorm-recon-agent.md
var brainstormReconAgentContent []byte

//go:embed embed/brainstorm-codebase-agent.md
var brainstormCodebaseAgentContent []byte

//go:embed embed/brainstorm-trajectory-agent.md
var brainstormTrajectoryAgentContent []byte

//go:embed embed/brainstorm-opportunities-agent.md
var brainstormOpportunitiesAgentContent []byte

//go:embed embed/brainstorm-triage-agent.md
var brainstormTriageAgentContent []byte

//go:embed embed/human-ideate-skill.md
var ideateSkillContent []byte

//go:embed embed/human-ideator-agent.md
var ideatorAgentContent []byte

//go:embed embed/human-sprint-skill.md
var sprintSkillContent []byte

//go:embed embed/human-gardening-skill.md
var gardeningSkillContent []byte

//go:embed embed/gardening-survey-agent.md
var gardeningSurveyAgentContent []byte

//go:embed embed/gardening-structure-agent.md
var gardeningStructureAgentContent []byte

//go:embed embed/gardening-duplication-agent.md
var gardeningDuplicationAgentContent []byte

//go:embed embed/gardening-complexity-agent.md
var gardeningComplexityAgentContent []byte

//go:embed embed/gardening-hygiene-agent.md
var gardeningHygieneAgentContent []byte

//go:embed embed/gardening-triage-agent.md
var gardeningTriageAgentContent []byte

//go:embed embed/human-autofix-skill.md
var autofixSkillContent []byte

//go:embed embed/human-bug-triage-agent.md
var bugTriageAgentContent []byte

//go:embed embed/human-preflight-agent.md
var preflightAgentContent []byte

//go:embed embed/human-verdict-skeptic-agent.md
var verdictSkepticAgentContent []byte

//go:embed embed/human-bug-fixer-agent.md
var bugFixerAgentContent []byte

//go:embed embed/human-bug-verify-agent.md
var bugVerifyAgentContent []byte

//go:embed embed/codenav-skill.md
var codenavSkillContent []byte

//go:embed embed/human-features-skill.md
var featuresSkillContent []byte

//go:embed embed/features-recon-agent.md
var featuresReconAgentContent []byte

//go:embed embed/features-synthesis-agent.md
var featuresSynthesisAgentContent []byte

//go:embed embed/human-mockups-skill.md
var mockupsSkillContent []byte

var userHomeDir = os.UserHomeDir

// FileWriter abstracts filesystem operations for testability.
type FileWriter interface {
	MkdirAll(path string, perm os.FileMode) error
	WriteFile(name string, data []byte, perm os.FileMode) error
	ReadFile(name string) ([]byte, error)
}

// OSFileWriter implements FileWriter using the os package.
type OSFileWriter struct{}

func (OSFileWriter) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (OSFileWriter) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

func (OSFileWriter) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Clean(name))
}

type embeddedFile struct {
	content []byte
	relPath string
}

// Install writes the Claude Code skill and agent files to disk.
// When personal is true, files are written under ~/.claude/ instead of .claude/.
func Install(w io.Writer, fw FileWriter, personal bool) error {
	baseDir := ".claude"
	if personal {
		home, err := userHomeDir()
		if err != nil {
			return errors.WrapWithDetails(err, "resolving home directory")
		}
		baseDir = filepath.Join(home, ".claude")
	}

	files := []embeddedFile{
		{content: skillContent, relPath: filepath.Join("skills", "human-plan", "SKILL.md")},
		{content: agentContent, relPath: filepath.Join("agents", "human-planner.md")},
		{content: planVerifyCodeAgentContent, relPath: filepath.Join("agents", "plan-verify-code.md")},
		{content: planVerifyDocsAgentContent, relPath: filepath.Join("agents", "plan-verify-docs.md")},
		{content: readySkillContent, relPath: filepath.Join("skills", "human-ready", "SKILL.md")},
		{content: readyAgentContent, relPath: filepath.Join("agents", "human-ready.md")},
		{content: bugPlanSkillContent, relPath: filepath.Join("skills", "human-bug-plan", "SKILL.md")},
		{content: bugAnalyzerAgentContent, relPath: filepath.Join("agents", "human-bug-analyzer.md")},
		{content: reviewSkillContent, relPath: filepath.Join("skills", "human-review", "SKILL.md")},
		{content: reviewerAgentContent, relPath: filepath.Join("agents", "human-reviewer.md")},
		{content: doneAgentContent, relPath: filepath.Join("agents", "human-done.md")},
		{content: executeSkillContent, relPath: filepath.Join("skills", "human-execute", "SKILL.md")},
		{content: executorAgentContent, relPath: filepath.Join("agents", "human-executor.md")},
		{content: findbugsSkillContent, relPath: filepath.Join("skills", "human-findbugs", "SKILL.md")},
		{content: findbugsReconAgentContent, relPath: filepath.Join("agents", "findbugs-recon.md")},
		{content: findbugsLogicAgentContent, relPath: filepath.Join("agents", "findbugs-logic.md")},
		{content: findbugsErrorsAgentContent, relPath: filepath.Join("agents", "findbugs-errors.md")},
		{content: findbugsConcurrencyAgentContent, relPath: filepath.Join("agents", "findbugs-concurrency.md")},
		{content: findbugsAPIAgentContent, relPath: filepath.Join("agents", "findbugs-api.md")},
		{content: findbugsTriageAgentContent, relPath: filepath.Join("agents", "findbugs-triage.md")},
		{content: securitySkillContent, relPath: filepath.Join("skills", "human-security", "SKILL.md")},
		{content: securitySurfaceAgentContent, relPath: filepath.Join("agents", "security-surface.md")},
		{content: securityInjectionAgentContent, relPath: filepath.Join("agents", "security-injection.md")},
		{content: securityAuthAgentContent, relPath: filepath.Join("agents", "security-auth.md")},
		{content: securitySecretsAgentContent, relPath: filepath.Join("agents", "security-secrets.md")},
		{content: securityDepsAgentContent, relPath: filepath.Join("agents", "security-deps.md")},
		{content: securityInfraAgentContent, relPath: filepath.Join("agents", "security-infra.md")},
		{content: securityChainsAgentContent, relPath: filepath.Join("agents", "security-chains.md")},
		{content: securityTriageAgentContent, relPath: filepath.Join("agents", "security-triage.md")},
		{content: brainstormSkillContent, relPath: filepath.Join("skills", "human-brainstorm", "SKILL.md")},
		{content: brainstormReconAgentContent, relPath: filepath.Join("agents", "brainstorm-recon.md")},
		{content: brainstormCodebaseAgentContent, relPath: filepath.Join("agents", "brainstorm-codebase.md")},
		{content: brainstormTrajectoryAgentContent, relPath: filepath.Join("agents", "brainstorm-trajectory.md")},
		{content: brainstormOpportunitiesAgentContent, relPath: filepath.Join("agents", "brainstorm-opportunities.md")},
		{content: brainstormTriageAgentContent, relPath: filepath.Join("agents", "brainstorm-triage.md")},
		{content: ideateSkillContent, relPath: filepath.Join("skills", "human-ideate", "SKILL.md")},
		{content: ideatorAgentContent, relPath: filepath.Join("agents", "human-ideator.md")},
		{content: sprintSkillContent, relPath: filepath.Join("skills", "human-sprint", "SKILL.md")},
		{content: gardeningSkillContent, relPath: filepath.Join("skills", "human-gardening", "SKILL.md")},
		{content: gardeningSurveyAgentContent, relPath: filepath.Join("agents", "gardening-survey.md")},
		{content: gardeningStructureAgentContent, relPath: filepath.Join("agents", "gardening-structure.md")},
		{content: gardeningDuplicationAgentContent, relPath: filepath.Join("agents", "gardening-duplication.md")},
		{content: gardeningComplexityAgentContent, relPath: filepath.Join("agents", "gardening-complexity.md")},
		{content: gardeningHygieneAgentContent, relPath: filepath.Join("agents", "gardening-hygiene.md")},
		{content: gardeningTriageAgentContent, relPath: filepath.Join("agents", "gardening-triage.md")},
		{content: autofixSkillContent, relPath: filepath.Join("skills", "human-autofix", "SKILL.md")},
		{content: bugTriageAgentContent, relPath: filepath.Join("agents", "human-bug-triage.md")},
		{content: preflightAgentContent, relPath: filepath.Join("agents", "human-preflight.md")},
		{content: verdictSkepticAgentContent, relPath: filepath.Join("agents", "human-verdict-skeptic.md")},
		{content: bugFixerAgentContent, relPath: filepath.Join("agents", "human-bug-fixer.md")},
		{content: bugVerifyAgentContent, relPath: filepath.Join("agents", "human-bug-verify.md")},
		{content: codenavSkillContent, relPath: filepath.Join("skills", "codenav", "SKILL.md")},
		{content: featuresSkillContent, relPath: filepath.Join("skills", "human-features", "SKILL.md")},
		{content: featuresReconAgentContent, relPath: filepath.Join("agents", "features-recon.md")},
		{content: featuresSynthesisAgentContent, relPath: filepath.Join("agents", "features-synthesis.md")},
		{content: mockupsSkillContent, relPath: filepath.Join("skills", "human-mockups", "SKILL.md")},
	}

	for _, f := range files {
		dest := filepath.Join(baseDir, f.relPath)

		if err := fw.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return errors.WrapWithDetails(err, "creating directory",
				"path", filepath.Dir(dest))
		}

		_, err := fw.ReadFile(dest)
		action := "Created"
		if err == nil {
			action = "Overwriting"
		}

		body, err := expandIncludes(f.content)
		if err != nil {
			return errors.WrapWithDetails(err, "expanding shared fragments", "path", dest)
		}

		if err := fw.WriteFile(dest, body, 0o644); err != nil {
			return errors.WrapWithDetails(err, "writing file",
				"path", dest)
		}

		_, _ = fmt.Fprintf(w, "  %s %s\n", action, dest)
	}

	// Install hooks into ~/.claude/settings.json (always — hooks are global).
	_, _ = fmt.Fprintln(w, "\nInstalling Claude Code hooks...")
	if err := InstallHooks(w, fw); err != nil {
		return err
	}

	return nil
}
