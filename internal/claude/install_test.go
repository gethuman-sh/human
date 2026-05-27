package claude

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

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

	assert.Equal(t, string(skillContent), string(fw.files[skillPath]))
	assert.Equal(t, string(agentContent), string(fw.files[agentPath]))
	assert.Equal(t, string(planVerifyCodeAgentContent), string(fw.files[planVerifyCodeAgentPath]))
	assert.Equal(t, string(planVerifyDocsAgentContent), string(fw.files[planVerifyDocsAgentPath]))
	assert.Equal(t, string(readySkillContent), string(fw.files[readySkillPath]))
	assert.Equal(t, string(readyAgentContent), string(fw.files[readyAgentPath]))
	assert.Equal(t, string(bugPlanSkillContent), string(fw.files[bugPlanSkillPath]))
	assert.Equal(t, string(bugAnalyzerAgentContent), string(fw.files[bugAnalyzerAgentPath]))
	assert.Equal(t, string(reviewSkillContent), string(fw.files[reviewSkillPath]))
	assert.Equal(t, string(reviewerAgentContent), string(fw.files[reviewerAgentPath]))
	assert.Equal(t, string(doneAgentContent), string(fw.files[doneAgentPath]))
	assert.Equal(t, string(executeSkillContent), string(fw.files[executeSkillPath]))
	assert.Equal(t, string(executorAgentContent), string(fw.files[executorAgentPath]))
	assert.Equal(t, string(findbugsSkillContent), string(fw.files[findbugsSkillPath]))
	assert.Equal(t, string(findbugsReconAgentContent), string(fw.files[findbugsReconAgentPath]))
	assert.Equal(t, string(findbugsLogicAgentContent), string(fw.files[findbugsLogicAgentPath]))
	assert.Equal(t, string(findbugsErrorsAgentContent), string(fw.files[findbugsErrorsAgentPath]))
	assert.Equal(t, string(findbugsConcurrencyAgentContent), string(fw.files[findbugsConcurrencyAgentPath]))
	assert.Equal(t, string(findbugsAPIAgentContent), string(fw.files[findbugsAPIAgentPath]))
	assert.Equal(t, string(findbugsTriageAgentContent), string(fw.files[findbugsTriageAgentPath]))
	assert.Equal(t, string(securitySkillContent), string(fw.files[securitySkillPath]))
	assert.Equal(t, string(securitySurfaceAgentContent), string(fw.files[securitySurfaceAgentPath]))
	assert.Equal(t, string(securityInjectionAgentContent), string(fw.files[securityInjectionAgentPath]))
	assert.Equal(t, string(securityAuthAgentContent), string(fw.files[securityAuthAgentPath]))
	assert.Equal(t, string(securitySecretsAgentContent), string(fw.files[securitySecretsAgentPath]))
	assert.Equal(t, string(securityDepsAgentContent), string(fw.files[securityDepsAgentPath]))
	assert.Equal(t, string(securityInfraAgentContent), string(fw.files[securityInfraAgentPath]))
	assert.Equal(t, string(securityChainsAgentContent), string(fw.files[securityChainsAgentPath]))
	assert.Equal(t, string(securityTriageAgentContent), string(fw.files[securityTriageAgentPath]))
	assert.Equal(t, string(brainstormSkillContent), string(fw.files[brainstormSkillPath]))
	assert.Equal(t, string(brainstormReconAgentContent), string(fw.files[brainstormReconAgentPath]))
	assert.Equal(t, string(brainstormCodebaseAgentContent), string(fw.files[brainstormCodebaseAgentPath]))
	assert.Equal(t, string(brainstormTrajectoryAgentContent), string(fw.files[brainstormTrajectoryAgentPath]))
	assert.Equal(t, string(brainstormOpportunitiesAgentContent), string(fw.files[brainstormOpportunitiesAgentPath]))
	assert.Equal(t, string(brainstormTriageAgentContent), string(fw.files[brainstormTriageAgentPath]))
	assert.Equal(t, string(ideateSkillContent), string(fw.files[ideateSkillPath]))
	assert.Equal(t, string(ideatorAgentContent), string(fw.files[ideatorAgentPath]))
	assert.Equal(t, string(sprintSkillContent), string(fw.files[sprintSkillPath]))
	assert.Equal(t, string(gardeningSkillContent), string(fw.files[gardeningSkillPath]))
	assert.Equal(t, string(gardeningSurveyAgentContent), string(fw.files[gardeningSurveyAgentPath]))
	assert.Equal(t, string(gardeningStructureAgentContent), string(fw.files[gardeningStructureAgentPath]))
	assert.Equal(t, string(gardeningDuplicationAgentContent), string(fw.files[gardeningDuplicationAgentPath]))
	assert.Equal(t, string(gardeningComplexityAgentContent), string(fw.files[gardeningComplexityAgentPath]))
	assert.Equal(t, string(gardeningHygieneAgentContent), string(fw.files[gardeningHygieneAgentPath]))
	assert.Equal(t, string(gardeningTriageAgentContent), string(fw.files[gardeningTriageAgentPath]))
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
	assert.Equal(t, string(skillContent), string(fw.files[skillPath]))
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

	// Verify files are written under the home directory, not ".claude"
	for path := range fw.files {
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
