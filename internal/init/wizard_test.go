package init

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"io"

	"github.com/StephanSchmidt/human/internal/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPrompter implements Prompter for testing.
type mockPrompter struct {
	overwrite               bool
	overwriteErr            error
	confirmAddTrackers      bool
	confirmAddTrackersErr   error
	selected                []ServiceType
	selectErr               error
	instanceValues          []map[string]string
	instanceErr             error
	installAgents           bool
	installErr              error
	promptIdx               int
	confirmDevcontainer     bool
	confirmDevcontainerErr  error
	confirmOverwriteDevcont bool
	confirmOverwriteDevErr  error
	confirmProxy            bool
	confirmProxyErr         error
	confirmIntercept        bool
	confirmInterceptErr     error
	selectedStacks          []StackType
	selectStacksErr         error
	selectedLsps            []LspPlugin
	selectLspsErr           error
	vaultProvider           string
	vaultProviderErr        error
	vaultAccount            string
	vaultAccountErr         error
}

func (m *mockPrompter) ConfirmOverwrite() (bool, error) {
	return m.overwrite, m.overwriteErr
}

func (m *mockPrompter) ConfirmAddTrackers() (bool, error) {
	return m.confirmAddTrackers, m.confirmAddTrackersErr
}

func (m *mockPrompter) SelectServices(_ []ServiceType) ([]ServiceType, error) {
	return m.selected, m.selectErr
}

func (m *mockPrompter) PromptInstance(_ ServiceType) (map[string]string, error) {
	if m.instanceErr != nil {
		return nil, m.instanceErr
	}
	if m.promptIdx >= len(m.instanceValues) {
		return map[string]string{"name": "work"}, nil
	}
	vals := m.instanceValues[m.promptIdx]
	m.promptIdx++
	return vals, nil
}

func (m *mockPrompter) ConfirmAgentInstall() (bool, error) {
	return m.installAgents, m.installErr
}

func (m *mockPrompter) ConfirmDevcontainer() (bool, error) {
	return m.confirmDevcontainer, m.confirmDevcontainerErr
}

func (m *mockPrompter) ConfirmOverwriteDevcontainer() (bool, error) {
	return m.confirmOverwriteDevcont, m.confirmOverwriteDevErr
}

func (m *mockPrompter) ConfirmProxy() (bool, error) {
	return m.confirmProxy, m.confirmProxyErr
}

func (m *mockPrompter) ConfirmIntercept() (bool, error) {
	return m.confirmIntercept, m.confirmInterceptErr
}

func (m *mockPrompter) SelectStacks(_ []StackType) ([]StackType, error) {
	return m.selectedStacks, m.selectStacksErr
}

func (m *mockPrompter) SelectLspPlugins(_ []LspPlugin) ([]LspPlugin, error) {
	return m.selectedLsps, m.selectLspsErr
}

func (m *mockPrompter) SelectVaultProvider(_ []string) (string, error) {
	return m.vaultProvider, m.vaultProviderErr
}

func (m *mockPrompter) PromptVaultAccount() (string, error) {
	return m.vaultAccount, m.vaultAccountErr
}

// mockFileWriter implements claude.FileWriter for testing.
type mockFileWriter struct {
	files map[string][]byte
}

func newMockFileWriter() *mockFileWriter {
	return &mockFileWriter{files: make(map[string][]byte)}
}

func (m *mockFileWriter) MkdirAll(_ string, _ os.FileMode) error { return nil }

func (m *mockFileWriter) WriteFile(name string, data []byte, _ os.FileMode) error {
	m.files[name] = data
	return nil
}

func (m *mockFileWriter) ReadFile(name string) ([]byte, error) {
	data, ok := m.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

// failingFileWriter always fails on WriteFile.
type failingFileWriter struct {
	err error
}

func (f *failingFileWriter) MkdirAll(_ string, _ os.FileMode) error            { return nil }
func (f *failingFileWriter) WriteFile(_ string, _ []byte, _ os.FileMode) error { return f.err }
func (f *failingFileWriter) ReadFile(_ string) ([]byte, error)                 { return nil, os.ErrNotExist }

// mockStep implements WizardStep for orchestrator-level tests.
type mockStep struct {
	name   string
	runFn  func(w io.Writer, fw claude.FileWriter) error
	hints  []string
	called bool
}

func (s *mockStep) Name() string { return s.name }

func (s *mockStep) Run(w io.Writer, fw claude.FileWriter) ([]string, error) {
	s.called = true
	if s.runFn != nil {
		return s.hints, s.runFn(w, fw)
	}
	return s.hints, nil
}

// --- Pure data tests (unchanged) ---

func TestServiceRegistry_AllServices(t *testing.T) {
	reg := ServiceRegistry()
	assert.Len(t, reg, 9)

	labels := make([]string, len(reg))
	for i, s := range reg {
		labels[i] = s.Label
	}
	assert.Contains(t, labels, "Jira")
	assert.Contains(t, labels, "GitHub")
	assert.Contains(t, labels, "GitLab")
	assert.Contains(t, labels, "Linear")
	assert.Contains(t, labels, "Azure DevOps")
	assert.Contains(t, labels, "Shortcut")
	assert.Contains(t, labels, "Notion")
	assert.Contains(t, labels, "Figma")
	assert.Contains(t, labels, "Amplitude")
}

func TestEnvVarName(t *testing.T) {
	tests := []struct {
		prefix, name, suffix, want string
	}{
		{"JIRA", "work", "KEY", "JIRA_WORK_KEY"},
		{"GITHUB", "oss", "TOKEN", "GITHUB_OSS_TOKEN"},
		{"AMPLITUDE", "product", "SECRET", "AMPLITUDE_PRODUCT_SECRET"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, EnvVarName(tt.prefix, tt.name, tt.suffix))
	}
}

func TestGenerateConfig_SingleJira(t *testing.T) {
	instances := []serviceInstance{
		{
			Service: ServiceType{
				Label: "Jira", ConfigKey: "jiras",
				EnvVars: []string{"KEY"}, EnvPrefix: "JIRA",
			},
			Values: map[string]string{
				"name": "work", "url": "https://myorg.atlassian.net",
				"user": "me@example.com", "description": "Product backlog",
			},
		},
	}

	got, err := GenerateConfig(instances)
	require.NoError(t, err)

	assert.Contains(t, got, "jiras:")
	assert.Contains(t, got, "name: work")
	assert.Contains(t, got, "url: https://myorg.atlassian.net")
	assert.Contains(t, got, "user: me@example.com")
	assert.Contains(t, got, "description: Product backlog")
	assert.Contains(t, got, "export JIRA_WORK_KEY=your-key")
}

func TestGenerateConfig_MultipleServices(t *testing.T) {
	instances := []serviceInstance{
		{
			Service: ServiceType{
				Label: "GitHub", ConfigKey: "githubs",
				EnvVars: []string{"TOKEN"}, EnvPrefix: "GITHUB",
			},
			Values: map[string]string{"name": "oss"},
		},
		{
			Service: ServiceType{
				Label: "Notion", ConfigKey: "notions",
				EnvVars: []string{"TOKEN"}, EnvPrefix: "NOTION",
			},
			Values: map[string]string{"name": "docs", "description": "Company docs"},
		},
	}

	got, err := GenerateConfig(instances)
	require.NoError(t, err)

	assert.Contains(t, got, "githubs:")
	assert.Contains(t, got, "notions:")
	// Sections should appear in order.
	assert.Less(t, strings.Index(got, "githubs:"), strings.Index(got, "notions:"))
}

func TestGenerateConfig_AmplitudeMultipleEnvVars(t *testing.T) {
	instances := []serviceInstance{
		{
			Service: ServiceType{
				Label: "Amplitude", ConfigKey: "amplitudes",
				EnvVars: []string{"KEY", "SECRET"}, EnvPrefix: "AMPLITUDE",
			},
			Values: map[string]string{"name": "product", "url": "https://amplitude.com"},
		},
	}

	got, err := GenerateConfig(instances)
	require.NoError(t, err)

	assert.Contains(t, got, "export AMPLITUDE_PRODUCT_KEY=your-key")
	assert.Contains(t, got, "export AMPLITUDE_PRODUCT_SECRET=your-secret")
}

func TestGenerateConfig_AzureDevOpsOrg(t *testing.T) {
	instances := []serviceInstance{
		{
			Service: ServiceType{
				Label: "Azure DevOps", ConfigKey: "azuredevops",
				ExtraFields: []string{"org"},
				EnvVars:     []string{"TOKEN"}, EnvPrefix: "AZURE",
			},
			Values: map[string]string{"name": "work", "org": "mycompany"},
		},
	}

	got, err := GenerateConfig(instances)
	require.NoError(t, err)

	assert.Contains(t, got, "org: mycompany")
}

func TestGenerateConfig_Empty(t *testing.T) {
	got, err := GenerateConfig(nil)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(got))
}

// --- Orchestrator (RunInit) tests ---

func TestRunInit_RunsAllSteps(t *testing.T) {
	step1 := &mockStep{name: "step1"}
	step2 := &mockStep{name: "step2"}
	var buf bytes.Buffer

	err := RunInit(&buf, []WizardStep{step1, step2}, newMockFileWriter())

	require.NoError(t, err)
	assert.True(t, step1.called)
	assert.True(t, step2.called)
	assert.Contains(t, buf.String(), "Done!")
}

func TestRunInit_StopsOnStepError(t *testing.T) {
	step1 := &mockStep{
		name: "failing",
		runFn: func(w io.Writer, fw claude.FileWriter) error {
			return fmt.Errorf("boom")
		},
	}
	step2 := &mockStep{name: "skipped"}
	var buf bytes.Buffer

	err := RunInit(&buf, []WizardStep{step1, step2}, newMockFileWriter())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wizard step")
	assert.Contains(t, err.Error(), "failing")
	assert.True(t, step1.called)
	assert.False(t, step2.called)
}

func TestRunInit_EmptySteps(t *testing.T) {
	var buf bytes.Buffer

	err := RunInit(&buf, nil, newMockFileWriter())

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Done!")
}

func TestRunInit_CollectsHints(t *testing.T) {
	step1 := &mockStep{name: "s1", hints: []string{"Install foo with: brew install foo"}}
	step2 := &mockStep{name: "s2", hints: []string{"Install bar with: npm install -g bar"}}
	step3 := &mockStep{name: "s3"} // no hints
	var buf bytes.Buffer

	err := RunInit(&buf, []WizardStep{step1, step2, step3}, newMockFileWriter())

	require.NoError(t, err)
	output := buf.String()
	assert.Contains(t, output, "Done!")
	assert.Contains(t, output, "Install foo with: brew install foo")
	assert.Contains(t, output, "Install bar with: npm install -g bar")
	// Hints appear after "Done!"
	doneIdx := strings.Index(output, "Done!")
	fooIdx := strings.Index(output, "Install foo")
	barIdx := strings.Index(output, "Install bar")
	assert.Greater(t, fooIdx, doneIdx)
	assert.Greater(t, barIdx, doneIdx)
}

// --- ServicesStep tests ---

func TestServicesStep_AddTrackersDeclined(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	prompter := &mockPrompter{confirmAddTrackers: false}
	step := NewServicesStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Skipping tracker configuration.")
	assert.Empty(t, fw.files)
}

func TestServicesStep_AddTrackersAccepted(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	registry := ServiceRegistry()
	prompter := &mockPrompter{
		confirmAddTrackers: true,
		selected:           []ServiceType{registry[1]},
		instanceValues:     []map[string]string{{"name": "oss"}},
	}
	step := NewServicesStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Wrote .humanconfig.yaml")
}

func TestServicesStep_AddTrackersError(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	prompter := &mockPrompter{confirmAddTrackersErr: fmt.Errorf("prompt error")}
	step := NewServicesStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirming tracker setup")
}

func TestServicesStep_NoServicesSelected(t *testing.T) {
	prompter := &mockPrompter{confirmAddTrackers: true, selected: nil}
	step := NewServicesStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No services selected")
	assert.Empty(t, fw.files)
}

func TestServicesStep_AbortOnExistingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".humanconfig.yaml"), []byte("existing"), 0o644))

	prompter := &mockPrompter{overwrite: false}
	step := NewServicesStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Aborted")
}

func TestServicesStep_FullFlow(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	registry := ServiceRegistry()
	jira := registry[0]
	github := registry[1]

	prompter := &mockPrompter{
		confirmAddTrackers: true,
		selected:           []ServiceType{jira, github},
		instanceValues: []map[string]string{
			{"name": "work", "url": "https://work.atlassian.net", "user": "dev@work.com", "description": "Work Jira"},
			{"name": "oss"},
		},
	}
	step := NewServicesStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Wrote .humanconfig.yaml")
	assert.Contains(t, buf.String(), "JIRA_WORK_KEY")
	assert.Contains(t, buf.String(), "GITHUB_OSS_TOKEN")

	yaml := string(fw.files[".humanconfig.yaml"])
	assert.Contains(t, yaml, "jiras:")
	assert.Contains(t, yaml, "githubs:")
}

func TestServicesStep_OverwriteError(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".humanconfig.yaml"), []byte("existing"), 0o644))

	prompter := &mockPrompter{overwriteErr: fmt.Errorf("input error")}
	step := NewServicesStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirming overwrite")
}

func TestServicesStep_SelectError(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	prompter := &mockPrompter{confirmAddTrackers: true, selectErr: fmt.Errorf("select error")}
	step := NewServicesStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "selecting services")
}

func TestServicesStep_InstancePromptError(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	registry := ServiceRegistry()
	prompter := &mockPrompter{
		confirmAddTrackers: true,
		selected:           []ServiceType{registry[0]},
		instanceErr:        fmt.Errorf("prompt error"),
	}
	step := NewServicesStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "configuring service")
}

func TestServicesStep_WriteError(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	registry := ServiceRegistry()
	prompter := &mockPrompter{
		confirmAddTrackers: true,
		selected:           []ServiceType{registry[1]},
		instanceValues:     []map[string]string{{"name": "oss"}},
	}
	step := NewServicesStep(prompter)
	failFw := &failingFileWriter{err: fmt.Errorf("disk full")}
	var buf bytes.Buffer

	_, err := step.Run(&buf, failFw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing config file")
}

func TestServicesStep_Name(t *testing.T) {
	step := NewServicesStep(&mockPrompter{})
	assert.Equal(t, "services", step.Name())
}

// --- AgentInstallStep tests ---

func TestAgentInstallStep_Declined(t *testing.T) {
	prompter := &mockPrompter{installAgents: false}
	step := NewAgentInstallStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "Installing")
}

func TestAgentInstallStep_Accepted(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	prompter := &mockPrompter{installAgents: true}
	step := NewAgentInstallStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Installing Claude Code integration")
}

func TestAgentInstallStep_PromptError(t *testing.T) {
	prompter := &mockPrompter{installErr: fmt.Errorf("install error")}
	step := NewAgentInstallStep(prompter)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirming agent install")
}

func TestAgentInstallStep_Name(t *testing.T) {
	step := NewAgentInstallStep(&mockPrompter{})
	assert.Equal(t, "agent-install", step.Name())
}

// --- DevcontainerStep tests ---

func TestDevcontainerStep_Name(t *testing.T) {
	step := NewDevcontainerStep(&mockPrompter{}, &WizardState{})
	assert.Equal(t, "devcontainer", step.Name())
}

func TestDevcontainerStep_Declined(t *testing.T) {
	prompter := &mockPrompter{confirmDevcontainer: false}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Empty(t, fw.files)
}

func TestDevcontainerStep_BasicConfig(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	prompter := &mockPrompter{
		confirmDevcontainer: true,
		confirmProxy:        false,
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	hints, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Wrote .devcontainer/devcontainer.json")
	assert.Contains(t, hints, "  Start container:  human devcontainer up")

	data := string(fw.files[".devcontainer/devcontainer.json"])
	assert.Contains(t, data, "mcr.microsoft.com/devcontainers/base:ubuntu")
	assert.Contains(t, data, "ghcr.io/devcontainers/features/node:1")
	assert.Contains(t, data, "ghcr.io/stephanschmidt/treehouse/human:1")
	assert.Contains(t, data, `"BROWSER": "human-browser"`)
	assert.NotContains(t, data, "ca.crt")
	assert.Contains(t, data, `"HUMAN_DAEMON_ADDR": "host.docker.internal:19285"`)
	assert.Contains(t, data, `"HUMAN_DAEMON_TOKEN": "${localEnv:HUMAN_DAEMON_TOKEN}"`)
	assert.Contains(t, data, `"HUMAN_CHROME_ADDR": "host.docker.internal:19286"`)
	assert.Contains(t, data, `"HUMAN_PROXY_ADDR": "host.docker.internal:19287"`)
	assert.NotContains(t, data, "capAdd")
	assert.Contains(t, data, "ghcr.io/anthropics/devcontainer-features/claude-code:1")
	assert.Contains(t, data, "human install --agent claude")
	assert.Contains(t, data, "human chrome-bridge")
	assert.NotContains(t, data, "human-proxy-setup")
}

func TestDevcontainerStep_WithProxy(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	prompter := &mockPrompter{
		confirmDevcontainer: true,
		confirmProxy:        true,
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)

	data := string(fw.files[".devcontainer/devcontainer.json"])
	assert.Contains(t, data, `"capAdd"`)
	assert.Contains(t, data, "NET_ADMIN")
	assert.Contains(t, data, "sudo -E human-proxy-setup")
	assert.Contains(t, data, "ghcr.io/anthropics/devcontainer-features/claude-code:1")
	assert.Contains(t, data, "human install --agent claude")
	assert.Contains(t, data, "human chrome-bridge")
	assert.Contains(t, data, `"proxy": true`)
	assert.Contains(t, data, `"BROWSER": "human-browser"`)
	assert.NotContains(t, data, "NODE_EXTRA_CA_CERTS")
	assert.NotContains(t, data, "update-ca-certificates")
}

func TestDevcontainerStep_WithProxyAndIntercept(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	prompter := &mockPrompter{
		confirmDevcontainer: true,
		confirmProxy:        true,
		confirmIntercept:    true,
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)

	data := string(fw.files[".devcontainer/devcontainer.json"])
	assert.Contains(t, data, `"capAdd"`)
	assert.Contains(t, data, "NET_ADMIN")
	assert.Contains(t, data, "sudo -E human-proxy-setup")
	assert.Contains(t, data, "NODE_EXTRA_CA_CERTS")
	assert.Contains(t, data, "update-ca-certificates")
	assert.Contains(t, data, ".human/ca.crt,target=/home/vscode/.human/ca.crt,type=bind,readonly")
	assert.Contains(t, data, "human install --agent claude")
}

func TestDevcontainerStep_OverwriteDeclined_InjectsFeature(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".devcontainer"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".devcontainer/devcontainer.json"), []byte(`{"image":"node:20"}`), 0o644))

	prompter := &mockPrompter{
		confirmDevcontainer:     true,
		confirmOverwriteDevcont: false,
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	fw.files[".devcontainer/devcontainer.json"] = []byte(`{"image":"node:20"}`)
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Added human feature to existing devcontainer config.")
	data := string(fw.files[".devcontainer/devcontainer.json"])
	assert.Contains(t, data, "ghcr.io/stephanschmidt/treehouse/human:1")
	assert.Contains(t, data, "node:20")
}

func TestDevcontainerStep_OverwriteDeclined_FeatureAlreadyPresent(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".devcontainer"), 0o755))
	existing := `{"features":{"ghcr.io/stephanschmidt/treehouse/human:1":{}}}`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".devcontainer/devcontainer.json"), []byte(existing), 0o644))

	prompter := &mockPrompter{
		confirmDevcontainer:     true,
		confirmOverwriteDevcont: false,
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	fw.files[".devcontainer/devcontainer.json"] = []byte(existing)
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "human feature already present")
}

func TestDevcontainerStep_OverwriteAccepted(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".devcontainer"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".devcontainer/devcontainer.json"), []byte("{}"), 0o644))

	prompter := &mockPrompter{
		confirmDevcontainer:     true,
		confirmOverwriteDevcont: true,
		confirmProxy:            false,
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Wrote .devcontainer/devcontainer.json")
	assert.NotEmpty(t, fw.files[".devcontainer/devcontainer.json"])
}

func TestDevcontainerStep_PromptError(t *testing.T) {
	prompter := &mockPrompter{confirmDevcontainerErr: fmt.Errorf("prompt failed")}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirming devcontainer creation")
}

func TestDevcontainerStep_OverwritePromptError(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".devcontainer"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".devcontainer/devcontainer.json"), []byte("{}"), 0o644))

	prompter := &mockPrompter{
		confirmDevcontainer:    true,
		confirmOverwriteDevErr: fmt.Errorf("overwrite error"),
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirming devcontainer overwrite")
}

func TestDevcontainerStep_ProxyPromptError(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	prompter := &mockPrompter{
		confirmDevcontainer: true,
		confirmProxyErr:     fmt.Errorf("proxy error"),
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirming proxy setup")
}

// --- StackRegistry tests ---

func TestStackRegistry_AllStacks(t *testing.T) {
	reg := StackRegistry()
	assert.Len(t, reg, 8)

	labels := make([]string, len(reg))
	for i, s := range reg {
		labels[i] = s.Label
	}
	assert.Contains(t, labels, "Go")
	assert.Contains(t, labels, "Rust")
	assert.Contains(t, labels, "Node.js 22 (required by Claude Code)")
	assert.Contains(t, labels, "Python")
	assert.Contains(t, labels, "Java")
	assert.Contains(t, labels, "Ruby")
	assert.Contains(t, labels, ".NET")
	assert.Contains(t, labels, "PHP")
}

func TestDevcontainerStep_WithStacks(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	reg := StackRegistry()
	prompter := &mockPrompter{
		confirmDevcontainer: true,
		confirmProxy:        false,
		selectedStacks:      []StackType{reg[1], reg[3]}, // Go, Python
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	data := string(fw.files[".devcontainer/devcontainer.json"])
	assert.Contains(t, data, "ghcr.io/devcontainers/features/go:1")
	assert.Contains(t, data, "ghcr.io/devcontainers/features/python:1")
	assert.Contains(t, data, "ghcr.io/stephanschmidt/treehouse/human:1")
}

func TestDevcontainerStep_StacksWithProxy(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	reg := StackRegistry()
	prompter := &mockPrompter{
		confirmDevcontainer: true,
		confirmProxy:        true,
		selectedStacks:      []StackType{reg[2]}, // Rust
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.NoError(t, err)
	data := string(fw.files[".devcontainer/devcontainer.json"])
	assert.Contains(t, data, "ghcr.io/devcontainers/features/rust:1")
	assert.Contains(t, data, `"proxy": true`)
	assert.Contains(t, data, "sudo -E human-proxy-setup")
	assert.Contains(t, data, "NET_ADMIN")
}

func TestDevcontainerStep_SelectStacksError(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	prompter := &mockPrompter{
		confirmDevcontainer: true,
		confirmProxy:        false,
		selectStacksErr:     fmt.Errorf("stack select error"),
	}
	step := NewDevcontainerStep(prompter, &WizardState{})
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "selecting language stacks")
}

func TestCheckDevcontainerPrereqs_ReturnsSlice(t *testing.T) {
	hints := checkDevcontainerPrereqs()
	// We can't control what's installed in CI, but verify the function
	// returns a slice and each entry mentions a known tool.
	for _, hint := range hints {
		assert.True(t,
			strings.Contains(hint, "Docker") || strings.Contains(hint, "devcontainer"),
			"unexpected hint: %s", hint)
	}
}

// mockLspInstaller implements LspInstaller for testing.
type mockLspInstaller struct {
	installed          map[string]bool
	binaryInstallErr   error
	binaryInstallCalls []string
	pluginInstallErr   error
	pluginInstallCalls []string
	marketplaceErr     error
	marketplaceCalls   []string
}

func (m *mockLspInstaller) IsInstalled(binary string) bool {
	return m.installed[binary]
}

func (m *mockLspInstaller) Install(cmd string) error {
	m.binaryInstallCalls = append(m.binaryInstallCalls, cmd)
	return m.binaryInstallErr
}

func (m *mockLspInstaller) InstallPlugin(pluginID string) error {
	m.pluginInstallCalls = append(m.pluginInstallCalls, pluginID)
	return m.pluginInstallErr
}

func (m *mockLspInstaller) EnsureMarketplace(repo string) error {
	m.marketplaceCalls = append(m.marketplaceCalls, repo)
	return m.marketplaceErr
}

// --- Integration: full wizard with real steps ---

func TestRunInit_FullWizardFlow(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	registry := ServiceRegistry()
	jira := registry[0]
	github := registry[1]

	prompter := &mockPrompter{
		confirmAddTrackers: true,
		selected:           []ServiceType{jira, github},
		instanceValues: []map[string]string{
			{"name": "work", "url": "https://work.atlassian.net", "user": "dev@work.com", "description": "Work Jira"},
			{"name": "oss"},
		},
		confirmDevcontainer: true,
		confirmProxy:        false,
		installAgents:       false,
	}

	installer := &mockLspInstaller{installed: map[string]bool{}}
	steps := []WizardStep{
		NewServicesStep(prompter),
		NewDevcontainerStep(prompter, &WizardState{}),
		NewLspSetupStep(prompter, installer, &WizardState{}),
		NewAgentInstallStep(prompter),
	}
	fw := newMockFileWriter()
	var buf bytes.Buffer

	err := RunInit(&buf, steps, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Wrote .humanconfig.yaml")
	assert.Contains(t, buf.String(), "JIRA_WORK_KEY")
	assert.Contains(t, buf.String(), "GITHUB_OSS_TOKEN")
	assert.Contains(t, buf.String(), "Wrote .devcontainer/devcontainer.json")
	assert.Contains(t, buf.String(), "Done!")

	yaml := string(fw.files[".humanconfig.yaml"])
	assert.Contains(t, yaml, "jiras:")
	assert.Contains(t, yaml, "githubs:")

	dcJSON := string(fw.files[".devcontainer/devcontainer.json"])
	assert.Contains(t, dcJSON, "ghcr.io/stephanschmidt/treehouse/human:1")
}

func TestRunInit_FullWizardWithAgentInstall(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(tmpDir))

	registry := ServiceRegistry()
	prompter := &mockPrompter{
		confirmAddTrackers:  true,
		selected:            []ServiceType{registry[1]}, // GitHub
		instanceValues:      []map[string]string{{"name": "oss"}},
		confirmDevcontainer: false,
		installAgents:       true,
	}

	installer := &mockLspInstaller{installed: map[string]bool{}}
	steps := []WizardStep{
		NewServicesStep(prompter),
		NewDevcontainerStep(prompter, &WizardState{}),
		NewLspSetupStep(prompter, installer, &WizardState{}),
		NewAgentInstallStep(prompter),
	}
	fw := newMockFileWriter()
	var buf bytes.Buffer

	err := RunInit(&buf, steps, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Installing Claude Code integration")
	assert.Contains(t, buf.String(), "Done!")
}

// --- ProjectConfigStep tests ---

func TestProjectConfigStep_Name(t *testing.T) {
	step := NewProjectConfigStep(&WizardState{})
	assert.Equal(t, "project-config", step.Name())
}

func TestProjectConfigStep_CreatesMinimalConfig(t *testing.T) {
	fw := newMockFileWriter()
	var buf bytes.Buffer

	hints, err := NewProjectConfigStep(&WizardState{}).Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	data, ok := fw.files[".humanconfig.yaml"]
	require.True(t, ok, "expected .humanconfig.yaml to be written")
	content := string(data)
	assert.Contains(t, content, "project:")
	assert.NotContains(t, content, "devcontainer:")
}

func TestProjectConfigStep_WithDevcontainer(t *testing.T) {
	fw := newMockFileWriter()
	fw.files[".devcontainer/devcontainer.json"] = []byte(`{"name":"test"}`)
	var buf bytes.Buffer

	hints, err := NewProjectConfigStep(&WizardState{}).Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	content := string(fw.files[".humanconfig.yaml"])
	assert.Contains(t, content, "project:")
	assert.Contains(t, content, "devcontainer:\n  configdir:")
}

func TestProjectConfigStep_AppendsToExisting(t *testing.T) {
	fw := newMockFileWriter()
	fw.files[".humanconfig.yaml"] = []byte("jiras:\n  - name: work\n")
	fw.files[".devcontainer/devcontainer.json"] = []byte(`{}`)
	var buf bytes.Buffer

	hints, err := NewProjectConfigStep(&WizardState{}).Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	content := string(fw.files[".humanconfig.yaml"])
	assert.Contains(t, content, "jiras:")
	assert.Contains(t, content, "project:")
	assert.Contains(t, content, "devcontainer:")
}

func TestProjectConfigStep_SkipsExistingKeys(t *testing.T) {
	fw := newMockFileWriter()
	original := "project: myapp\ndevcontainer:\n  configdir: \".\"\n"
	fw.files[".humanconfig.yaml"] = []byte(original)
	fw.files[".devcontainer/devcontainer.json"] = []byte(`{}`)
	var buf bytes.Buffer

	hints, err := NewProjectConfigStep(&WizardState{}).Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	// File should not have been rewritten.
	assert.Equal(t, original, string(fw.files[".humanconfig.yaml"]))
}

func TestProjectConfigStep_WriteError(t *testing.T) {
	fw := &failingFileWriter{err: fmt.Errorf("disk full")}
	var buf bytes.Buffer

	_, err := NewProjectConfigStep(&WizardState{}).Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing project config")
}

func TestProjectConfigStep_WithProxy(t *testing.T) {
	fw := newMockFileWriter()
	state := &WizardState{ProxyEnabled: true}
	var buf bytes.Buffer

	hints, err := NewProjectConfigStep(state).Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	content := string(fw.files[".humanconfig.yaml"])
	assert.Contains(t, content, "proxy:\n  mode: allowlist\n  domains:")
	for _, d := range DefaultProxyDomains {
		assert.Contains(t, content, d)
	}
	assert.NotContains(t, content, "intercept:")
}

func TestProjectConfigStep_WithProxyAndIntercept(t *testing.T) {
	fw := newMockFileWriter()
	state := &WizardState{ProxyEnabled: true, InterceptEnabled: true}
	var buf bytes.Buffer

	hints, err := NewProjectConfigStep(state).Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	content := string(fw.files[".humanconfig.yaml"])
	assert.Contains(t, content, "proxy:\n  mode: allowlist\n  domains:")
	assert.Contains(t, content, "intercept:")
	assert.Contains(t, content, "api.anthropic.com")
}

func TestProjectConfigStep_ProxySkippedWhenAlreadyPresent(t *testing.T) {
	fw := newMockFileWriter()
	original := "project: myapp\nproxy:\n  mode: blocklist\n"
	fw.files[".humanconfig.yaml"] = []byte(original)
	state := &WizardState{ProxyEnabled: true}
	var buf bytes.Buffer

	hints, err := NewProjectConfigStep(state).Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	// Proxy already exists, should not be duplicated.
	assert.Equal(t, original, string(fw.files[".humanconfig.yaml"]))
}

func TestProjectConfigStep_AbsoluteConfigdir(t *testing.T) {
	fw := newMockFileWriter()
	fw.files[".devcontainer/devcontainer.json"] = []byte(`{"name":"test"}`)
	var buf bytes.Buffer

	hints, err := NewProjectConfigStep(&WizardState{}).Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	content := string(fw.files[".humanconfig.yaml"])
	assert.Contains(t, content, "devcontainer:\n  configdir:")
	// configdir must be an absolute path, not "."
	assert.NotContains(t, content, `configdir: "."`)
	assert.Contains(t, content, "configdir: /")
}

func TestGenerateProxyYAML(t *testing.T) {
	yaml := generateProxyYAML(false)
	assert.Contains(t, yaml, "proxy:")
	assert.Contains(t, yaml, "mode: allowlist")
	assert.Contains(t, yaml, "*.github.com")
	assert.Contains(t, yaml, "*.anthropic.com")
	assert.NotContains(t, yaml, "intercept:")
}

func TestGenerateProxyYAML_WithIntercept(t *testing.T) {
	yaml := generateProxyYAML(true)
	assert.Contains(t, yaml, "intercept:")
	assert.Contains(t, yaml, "api.anthropic.com")
}

// --- VaultStep tests ---

func TestVaultStep_Name(t *testing.T) {
	step := NewVaultStep(&mockPrompter{}, &WizardState{})
	assert.Equal(t, "vault", step.Name())
}

func TestVaultStep_NoneSelected(t *testing.T) {
	prompter := &mockPrompter{vaultProvider: ""}
	state := &WizardState{}
	step := NewVaultStep(prompter, state)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	hints, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	assert.Empty(t, state.VaultProvider)
}

func TestVaultStep_OnePasswordSelected(t *testing.T) {
	prompter := &mockPrompter{
		vaultProvider: "1password",
		vaultAccount:  "my-team",
	}
	state := &WizardState{}
	step := NewVaultStep(prompter, state)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	hints, err := step.Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	assert.Equal(t, "1password", state.VaultProvider)
	assert.Equal(t, "my-team", state.VaultAccount)
	assert.Contains(t, buf.String(), "1password")
	assert.Contains(t, buf.String(), "my-team")
}

func TestVaultStep_SelectError(t *testing.T) {
	prompter := &mockPrompter{vaultProviderErr: fmt.Errorf("prompt error")}
	state := &WizardState{}
	step := NewVaultStep(prompter, state)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "selecting vault provider")
}

func TestVaultStep_AccountError(t *testing.T) {
	prompter := &mockPrompter{
		vaultProvider:   "1password",
		vaultAccountErr: fmt.Errorf("prompt error"),
	}
	state := &WizardState{}
	step := NewVaultStep(prompter, state)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	_, err := step.Run(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompting vault account")
}

func TestProjectConfigStep_WithVault(t *testing.T) {
	fw := newMockFileWriter()
	state := &WizardState{VaultProvider: "1password", VaultAccount: "my-team"}
	var buf bytes.Buffer

	hints, err := NewProjectConfigStep(state).Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	content := string(fw.files[".humanconfig.yaml"])
	assert.Contains(t, content, "vault:\n  provider: 1password\n  account: my-team")
}

func TestProjectConfigStep_WithVaultNoAccount(t *testing.T) {
	fw := newMockFileWriter()
	state := &WizardState{VaultProvider: "1password"}
	var buf bytes.Buffer

	hints, err := NewProjectConfigStep(state).Run(&buf, fw)

	require.NoError(t, err)
	assert.Nil(t, hints)
	content := string(fw.files[".humanconfig.yaml"])
	assert.Contains(t, content, "vault:\n  provider: 1password")
	assert.NotContains(t, content, "account:")
}

func TestGenerateVaultYAML(t *testing.T) {
	yaml := generateVaultYAML("1password", "my-team")
	assert.Contains(t, yaml, "vault:")
	assert.Contains(t, yaml, "provider: 1password")
	assert.Contains(t, yaml, "account: my-team")
}

func TestGenerateVaultYAML_NoAccount(t *testing.T) {
	yaml := generateVaultYAML("1password", "")
	assert.Contains(t, yaml, "vault:")
	assert.Contains(t, yaml, "provider: 1password")
	assert.NotContains(t, yaml, "account:")
}

func TestHasYAMLKey(t *testing.T) {
	content := "jiras:\n  - name: work\nproject: myapp\n"
	assert.True(t, hasYAMLKey(content, "project"))
	assert.True(t, hasYAMLKey(content, "jiras"))
	assert.False(t, hasYAMLKey(content, "devcontainer"))
	assert.False(t, hasYAMLKey(content, ""))
}
