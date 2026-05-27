package init

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude"
)

// LspPlugin describes an LSP server and its Claude Code plugin.
type LspPlugin struct {
	Label       string // display name, e.g. "gopls (Go)"
	PluginID    string // e.g. "gopls@claude-code-lsps"
	Binary      string // binary name to check on PATH
	InstallCmd  string // auto-install command for the LSP binary (empty = manual only)
	InstallHint string // manual install hint for the LSP binary
}

// LspRegistry returns all available LSP plugins.
func LspRegistry() []LspPlugin {
	return []LspPlugin{
		{
			Label:       "gopls (Go)",
			PluginID:    "gopls@claude-code-lsps",
			Binary:      "gopls",
			InstallCmd:  "go install golang.org/x/tools/gopls@latest",
			InstallHint: "go install golang.org/x/tools/gopls@latest",
		},
		{
			Label:       "rust-analyzer (Rust)",
			PluginID:    "rust-analyzer@claude-code-lsps",
			Binary:      "rust-analyzer",
			InstallCmd:  "rustup component add rust-analyzer",
			InstallHint: "rustup component add rust-analyzer",
		},
		{
			Label:       "pyright (Python)",
			PluginID:    "pyright@claude-code-lsps",
			Binary:      "pyright-langserver",
			InstallCmd:  "npm install -g pyright",
			InstallHint: "npm install -g pyright",
		},
		{
			Label:       "jdtls (Java)",
			PluginID:    "jdtls@claude-code-lsps",
			Binary:      "jdtls",
			InstallHint: "Download from https://download.eclipse.org/jdtls/snapshots/",
		},
		{
			Label:       "solargraph (Ruby)",
			PluginID:    "solargraph@claude-code-lsps",
			Binary:      "solargraph",
			InstallCmd:  "gem install solargraph",
			InstallHint: "gem install solargraph",
		},
		{
			Label:       "omnisharp (C#/.NET)",
			PluginID:    "omnisharp@claude-code-lsps",
			Binary:      "OmniSharp",
			InstallHint: "Download from https://github.com/OmniSharp/omnisharp-roslyn/releases",
		},
		{
			Label:       "intelephense (PHP)",
			PluginID:    "intelephense@claude-code-lsps",
			Binary:      "intelephense",
			InstallCmd:  "npm install -g intelephense",
			InstallHint: "npm install -g intelephense",
		},
		{
			Label:       "vtsls (TypeScript/JS)",
			PluginID:    "vtsls@claude-code-lsps",
			Binary:      "vtsls",
			InstallCmd:  "npm install -g @vtsls/language-server",
			InstallHint: "npm install -g @vtsls/language-server",
		},
		{
			Label:       "bash-language-server (Bash)",
			PluginID:    "bash-language-server@claude-code-lsps",
			Binary:      "bash-language-server",
			InstallCmd:  "npm install -g bash-language-server",
			InstallHint: "npm install -g bash-language-server",
		},
	}
}

// LspPrompter abstracts TUI interactions for the LSP setup step.
type LspPrompter interface {
	SelectLspPlugins(available []LspPlugin) ([]LspPlugin, error)
}

// LspInstaller abstracts checking/installing LSP binaries and Claude Code plugins.
type LspInstaller interface {
	IsInstalled(binary string) bool
	Install(cmd string) error
	InstallPlugin(pluginID string) error
	EnsureMarketplace(repo string) error
}

// OSLspInstaller implements LspInstaller using os/exec.
type OSLspInstaller struct{}

// IsInstalled checks whether the given binary is on PATH.
func (OSLspInstaller) IsInstalled(binary string) bool {
	_, err := exec.LookPath(binary)
	return err == nil
}

// Install runs the given install command for an LSP binary.
func (OSLspInstaller) Install(cmd string) error {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return errors.WithDetails("empty install command")
	}
	// #nosec G204 -- commands come from hardcoded LspRegistry only
	return exec.Command(parts[0], parts[1:]...).Run()
}

// InstallPlugin installs a Claude Code plugin via the claude CLI.
func (OSLspInstaller) InstallPlugin(pluginID string) error {
	// #nosec G204 -- pluginID comes from hardcoded LspRegistry only
	return exec.Command("claude", "plugin", "install", pluginID).Run()
}

// EnsureMarketplace adds a marketplace to Claude Code if not already present.
func (OSLspInstaller) EnsureMarketplace(repo string) error {
	// #nosec G204 -- repo comes from hardcoded constant only
	return exec.Command("claude", "plugin", "marketplace", "add", repo).Run()
}

const claudeCodeLspsRepo = "boostvolt/claude-code-lsps"

type lspSetupStep struct {
	prompter  LspPrompter
	installer LspInstaller
	state     *WizardState
}

// NewLspSetupStep creates a WizardStep that sets up LSP servers for Claude Code.
func NewLspSetupStep(p LspPrompter, installer LspInstaller, state *WizardState) WizardStep {
	return &lspSetupStep{
		prompter:  p,
		installer: installer,
		state:     state,
	}
}

func (s *lspSetupStep) Name() string { return "lsp-setup" }

func (s *lspSetupStep) Run(w io.Writer, fw claude.FileWriter) ([]string, error) {
	// Auto-select LSPs matching language stacks chosen in the devcontainer step.
	var selected []LspPlugin
	if len(s.state.SelectedStacks) > 0 {
		selected = lspsForStacks(s.state.SelectedStacks)
	}
	if len(selected) == 0 {
		var err error
		selected, err = s.prompter.SelectLspPlugins(LspRegistry())
		if err != nil {
			return nil, errors.WrapWithDetails(err, "selecting LSP plugins")
		}
	}
	if len(selected) == 0 {
		return nil, nil
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Setting up LSP servers for Claude Code...")

	// Ensure the community marketplace is registered.
	_, _ = fmt.Fprintln(w, "  Adding claude-code-lsps marketplace...")
	if mktErr := s.installer.EnsureMarketplace(claudeCodeLspsRepo); mktErr != nil {
		_, _ = fmt.Fprintln(w, "  Marketplace may already be registered, continuing...")
	}

	var hints []string
	var lspCmds []string
	for _, plugin := range selected {
		// Install the Claude Code plugin (downloads plugin files + enables it).
		_, _ = fmt.Fprintf(w, "  Installing plugin: %s\n", plugin.PluginID)
		if pluginErr := s.installer.InstallPlugin(plugin.PluginID); pluginErr != nil {
			hints = append(hints, fmt.Sprintf("Failed to install plugin %s. Install manually: claude plugin install %s", plugin.PluginID, plugin.PluginID))
			continue
		}

		// Track install command for devcontainer config.
		if plugin.InstallCmd != "" {
			lspCmds = append(lspCmds, plugin.InstallCmd)
		}

		// Check if LSP binary is already on PATH.
		if s.installer.IsInstalled(plugin.Binary) {
			_, _ = fmt.Fprintf(w, "  %s is already installed\n", plugin.Binary)
			continue
		}

		// Try to auto-install the LSP binary.
		if plugin.InstallCmd != "" {
			_, _ = fmt.Fprintf(w, "  Installing %s...\n", plugin.Binary)
			if installErr := s.installer.Install(plugin.InstallCmd); installErr != nil {
				hints = append(hints, fmt.Sprintf("Failed to install %s. Install manually: %s", plugin.Binary, plugin.InstallHint))
			} else {
				_, _ = fmt.Fprintf(w, "  %s installed successfully\n", plugin.Binary)
			}
		} else {
			hints = append(hints, fmt.Sprintf("Install %s manually: %s", plugin.Binary, plugin.InstallHint))
		}
	}

	// Update devcontainer config with LSP install commands so containers
	// get the LSP binaries installed on startup.
	if len(lspCmds) > 0 {
		appendLspToDevcontainer(w, fw, lspCmds)
	}

	// Enable the LSP tool in Claude Code settings.
	if err := enableLspTool(w, fw); err != nil {
		hints = append(hints, "Failed to enable LSP tool. Manually add '\"env\": {\"ENABLE_LSP_TOOL\": \"1\"}' to ~/.claude/settings.json")
	}

	return hints, nil
}

// lspsForStacks returns LSP plugins matching the given language stacks.
func lspsForStacks(stacks []StackType) []LspPlugin {
	mapping := StackToLspBinary()
	wanted := make(map[string]bool)
	for _, stack := range stacks {
		if binary, ok := mapping[stack.FeatureKey]; ok {
			wanted[binary] = true
		}
	}

	var result []LspPlugin
	for _, lsp := range LspRegistry() {
		if wanted[lsp.Binary] {
			result = append(result, lsp)
		}
	}
	return result
}

// userHomeDir is a package-level variable for testability (matches claude package pattern).
var userHomeDir = os.UserHomeDir

// enableLspTool sets ENABLE_LSP_TOOL=1 in ~/.claude/settings.json so that
// Claude Code exposes the LSP tool to the model.
func enableLspTool(w io.Writer, fw claude.FileWriter) error {
	home, err := userHomeDir()
	if err != nil {
		return errors.WrapWithDetails(err, "resolving home directory")
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	settings := make(map[string]interface{})

	data, err := fw.ReadFile(settingsPath)
	if err == nil {
		if jsonErr := json.Unmarshal(data, &settings); jsonErr != nil {
			return errors.WrapWithDetails(jsonErr, "parsing settings.json", "path", settingsPath)
		}
	} else if !os.IsNotExist(err) {
		return errors.WrapWithDetails(err, "reading settings.json", "path", settingsPath)
	}

	// Merge into the existing env map.
	envMap, _ := settings["env"].(map[string]interface{})
	if envMap == nil {
		envMap = make(map[string]interface{})
	}

	if envMap["ENABLE_LSP_TOOL"] == "1" {
		_, _ = fmt.Fprintf(w, "  ENABLE_LSP_TOOL already set in %s\n", settingsPath)
		return nil
	}

	envMap["ENABLE_LSP_TOOL"] = "1"
	settings["env"] = envMap

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling settings.json")
	}
	out = append(out, '\n')

	if err := fw.WriteFile(settingsPath, out, 0o644); err != nil {
		return errors.WrapWithDetails(err, "writing settings.json", "path", settingsPath)
	}

	_, _ = fmt.Fprintf(w, "  Enabled ENABLE_LSP_TOOL in %s\n", settingsPath)
	return nil
}

// appendLspToDevcontainer reads .devcontainer/devcontainer.json and appends
// LSP install commands to postStartCommand. Silently skips if no config exists.
func appendLspToDevcontainer(w io.Writer, fw claude.FileWriter, cmds []string) {
	data, err := fw.ReadFile(devcontainerPath)
	if err != nil {
		return
	}
	raw := map[string]interface{}{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	lspCmd := strings.Join(cmds, " && ")

	// Check if commands are already present.
	existing, _ := raw["postStartCommand"].(string)
	if strings.Contains(existing, lspCmd) {
		return
	}

	if existing != "" {
		raw["postStartCommand"] = existing + " && " + lspCmd
	} else {
		raw["postStartCommand"] = lspCmd
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	out = append(out, '\n')

	if err := fw.WriteFile(devcontainerPath, out, 0o644); err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "  Updated %s with LSP install commands\n", devcontainerPath)
}
