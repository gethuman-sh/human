package init

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/StephanSchmidt/human/errors"
	"github.com/StephanSchmidt/human/internal/claude"
	"github.com/StephanSchmidt/human/internal/daemon"
)

// DevcontainerPrompter abstracts TUI interactions for the devcontainer step.
type DevcontainerPrompter interface {
	ConfirmDevcontainer() (bool, error)
	ConfirmOverwriteDevcontainer() (bool, error)
	ConfirmProxy() (bool, error)
	ConfirmIntercept() (bool, error)
	SelectStacks(available []StackType) ([]StackType, error)
}

type devcontainerStep struct {
	prompter DevcontainerPrompter
	state    *WizardState
}

// NewDevcontainerStep creates a WizardStep that optionally generates .devcontainer/devcontainer.json.
func NewDevcontainerStep(p DevcontainerPrompter, state *WizardState) WizardStep {
	return &devcontainerStep{prompter: p, state: state}
}

func (s *devcontainerStep) Name() string { return "devcontainer" }

func (s *devcontainerStep) Run(w io.Writer, fw claude.FileWriter) ([]string, error) {
	create, err := s.prompter.ConfirmDevcontainer()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "confirming devcontainer creation")
	}
	if !create {
		return nil, nil
	}

	if _, err := os.Stat(devcontainerPath); err == nil {
		overwrite, promptErr := s.prompter.ConfirmOverwriteDevcontainer()
		if promptErr != nil {
			return nil, errors.WrapWithDetails(promptErr, "confirming devcontainer overwrite")
		}
		if !overwrite {
			hints, ensureErr := ensureHumanFeature(w, fw)
			if ensureErr != nil {
				return nil, ensureErr
			}
			return hints, nil
		}
	}

	proxy, err := s.prompter.ConfirmProxy()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "confirming proxy setup")
	}

	var intercept bool
	if proxy {
		intercept, err = s.prompter.ConfirmIntercept()
		if err != nil {
			return nil, errors.WrapWithDetails(err, "confirming traffic intercept")
		}
	}

	s.state.ProxyEnabled = proxy
	s.state.InterceptEnabled = intercept

	stacks, err := s.prompter.SelectStacks(StackRegistry())
	if err != nil {
		return nil, errors.WrapWithDetails(err, "selecting language stacks")
	}

	s.state.SelectedStacks = stacks

	cfg := buildDevcontainerConfig(proxy, intercept, stacks)

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling devcontainer config")
	}
	data = append(data, '\n')

	if err := fw.MkdirAll(devcontainerDir, 0o755); err != nil {
		return nil, errors.WrapWithDetails(err, "creating .devcontainer directory")
	}
	if err := fw.WriteFile(devcontainerPath, data, 0o644); err != nil {
		return nil, errors.WrapWithDetails(err, "writing devcontainer config",
			"path", devcontainerPath)
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "Wrote %s\n", devcontainerPath)

	hints := []string{
		"Next steps:",
		"  Start container:  human devcontainer up",
		"  (This auto-starts the daemon and injects all connectivity.)",
	}
	hints = append(hints, checkDevcontainerPrereqs()...)

	return hints, nil
}

const devcontainerDir = ".devcontainer"
const devcontainerPath = ".devcontainer/devcontainer.json"

type devcontainerConfig struct {
	Name             string                 `json:"name"`
	Image            string                 `json:"image"`
	RemoteUser       string                 `json:"remoteUser,omitempty"`
	Features         map[string]interface{} `json:"features"`
	Mounts           []string               `json:"mounts,omitempty"`
	RunArgs          []string               `json:"runArgs,omitempty"`
	CapAdd           []string               `json:"capAdd,omitempty"`
	ForwardPorts     []int                  `json:"forwardPorts"`
	RemoteEnv        map[string]string      `json:"remoteEnv,omitempty"`
	PostStartCommand string                 `json:"postStartCommand,omitempty"`
}

const humanFeatureKey = "ghcr.io/stephanschmidt/treehouse/human:1"
const claudeFeatureKey = "ghcr.io/anthropics/devcontainer-features/claude-code:1"
const nodeFeatureKey = "ghcr.io/devcontainers/features/node:1"

// ensureHumanFeature reads an existing devcontainer.json and adds the human
// feature if it is missing. Returns hints if the file was updated.
func ensureHumanFeature(w io.Writer, fw claude.FileWriter) ([]string, error) {
	data, err := fw.ReadFile(devcontainerPath)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "reading existing devcontainer config")
	}

	raw := map[string]interface{}{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errors.WrapWithDetails(err, "parsing existing devcontainer config")
	}

	features, _ := raw["features"].(map[string]interface{})
	if features != nil {
		if _, ok := features[humanFeatureKey]; ok {
			_, _ = fmt.Fprintln(w, "Keeping existing devcontainer config (human feature already present).")
			return nil, nil
		}
	}

	if features == nil {
		features = map[string]interface{}{}
		raw["features"] = features
	}
	features[humanFeatureKey] = map[string]interface{}{}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshalling updated devcontainer config")
	}
	out = append(out, '\n')

	if err := fw.WriteFile(devcontainerPath, out, 0o644); err != nil {
		return nil, errors.WrapWithDetails(err, "writing updated devcontainer config")
	}

	_, _ = fmt.Fprintln(w, "Added human feature to existing devcontainer config.")
	return nil, nil
}

// checkDevcontainerPrereqs returns hints for missing prerequisites (Docker, devcontainer CLI).
func checkDevcontainerPrereqs() []string {
	var hints []string
	if _, err := exec.LookPath("docker"); err != nil {
		hints = append(hints, "Docker is not installed. Install it from https://docs.docker.com/get-docker/")
	}
	if _, err := exec.LookPath("devcontainer"); err != nil {
		hints = append(hints, "devcontainer CLI is not installed. Install it with: npm install -g @devcontainers/cli")
	}
	return hints
}

func buildDevcontainerConfig(proxy, intercept bool, stacks []StackType) devcontainerConfig {
	featureOpts := map[string]interface{}{}
	if proxy {
		featureOpts["proxy"] = true
	}

	features := map[string]interface{}{
		nodeFeatureKey:   map[string]interface{}{"version": "22"},
		humanFeatureKey:  featureOpts,
		claudeFeatureKey: map[string]interface{}{},
	}
	for _, stack := range stacks {
		if stack.Fixed {
			continue // already added with pinned options above
		}
		features[stack.FeatureKey] = map[string]interface{}{}
	}

	cfg := devcontainerConfig{
		Name:         "human secure container",
		Image:        "mcr.microsoft.com/devcontainers/base:ubuntu",
		RemoteUser:   "vscode",
		Features:     features,
		RunArgs:      []string{"--add-host=host.docker.internal:host-gateway"},
		ForwardPorts: []int{19285, 19286},
		RemoteEnv: map[string]string{ // #nosec G101 -- not a credential, just env var name referencing localEnv
			"BROWSER":            "human-browser",
			"HUMAN_DAEMON_ADDR":  fmt.Sprintf("%s:%d", daemon.DockerHost, daemon.DefaultPort),
			"HUMAN_DAEMON_TOKEN": "${localEnv:HUMAN_DAEMON_TOKEN}",
			"HUMAN_CHROME_ADDR":  fmt.Sprintf("%s:%d", daemon.DockerHost, daemon.DefaultChromePort),
			"HUMAN_PROXY_ADDR":   fmt.Sprintf("%s:%d", daemon.DockerHost, daemon.DefaultProxyPort),
		},
	}

	switch {
	case proxy && intercept:
		cfg.Mounts = []string{"source=${localEnv:HOME}/.human/ca.crt,target=/home/vscode/.human/ca.crt,type=bind,readonly"}
		cfg.CapAdd = []string{"NET_ADMIN"}
		cfg.RemoteEnv["NODE_EXTRA_CA_CERTS"] = "/home/vscode/.human/ca.crt"
		cfg.PostStartCommand = "export HUMAN_PROXY_ADDR=$(getent hosts host.docker.internal | awk '{print $1}'):19287 && sudo -E human-proxy-setup && sudo cp /home/vscode/.human/ca.crt /usr/local/share/ca-certificates/human-proxy.crt && sudo update-ca-certificates && human install --agent claude && human chrome-bridge"
	case proxy:
		cfg.CapAdd = []string{"NET_ADMIN"}
		cfg.PostStartCommand = "export HUMAN_PROXY_ADDR=$(getent hosts host.docker.internal | awk '{print $1}'):19287 && sudo -E human-proxy-setup && human install --agent claude && human chrome-bridge"
	default:
		cfg.PostStartCommand = "human install --agent claude && human chrome-bridge"
	}

	// Install LSP binaries matching the selected language stacks.
	if lsp := lspInstallCmd(stacks); lsp != "" {
		cfg.PostStartCommand += " && " + lsp
	}

	return cfg
}

// lspInstallCmd returns a shell command that installs LSP server binaries
// for the selected language stacks. Returns "" when no stacks have an LSP.
func lspInstallCmd(stacks []StackType) string {
	featureToCmd := map[string]string{
		"ghcr.io/devcontainers/features/go:1":     "go install golang.org/x/tools/gopls@latest",
		"ghcr.io/devcontainers/features/rust:1":   "rustup component add rust-analyzer",
		"ghcr.io/devcontainers/features/python:1": "npm install -g pyright",
		"ghcr.io/devcontainers/features/ruby:1":   "gem install solargraph",
		"ghcr.io/devcontainers/features/php:1":    "npm install -g intelephense",
	}
	var cmds []string
	for _, stack := range stacks {
		if cmd, ok := featureToCmd[stack.FeatureKey]; ok {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return ""
	}
	return strings.Join(cmds, " && ")
}
