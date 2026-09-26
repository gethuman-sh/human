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
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/devcontainer"
	"github.com/gethuman-sh/human/internal/gotoolchain"
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

	// Only emit the ca.crt bind mount + NODE_EXTRA_CA_CERTS when a real,
	// PEM-parseable CA exists on the authoring host. Otherwise Docker
	// fabricates an empty directory at the bind source and Node's PEM parse
	// fails inside the container.
	caPresent := false
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		caPresent = devcontainer.IsValidCACertFile(filepath.Join(home, ".human", "ca.crt"))
	}

	cfg := buildDevcontainerConfig(proxy, intercept, stacks, caPresent, goModRequirement(fw))

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
	Name             string            `json:"name"`
	Image            string            `json:"image"`
	RemoteUser       string            `json:"remoteUser,omitempty"`
	Features         map[string]any    `json:"features"`
	Mounts           []string          `json:"mounts,omitempty"`
	RunArgs          []string          `json:"runArgs,omitempty"`
	CapAdd           []string          `json:"capAdd,omitempty"`
	ForwardPorts     []int             `json:"forwardPorts"`
	RemoteEnv        map[string]string `json:"remoteEnv,omitempty"`
	PostStartCommand string            `json:"postStartCommand,omitempty"`
}

const humanFeatureKey = "ghcr.io/gethuman-sh/treehouse/human:1"
const claudeFeatureKey = "ghcr.io/anthropics/devcontainer-features/claude-code:1"
const nodeFeatureKey = "ghcr.io/devcontainers/features/node:1"

// ensureHumanFeature reads an existing devcontainer.json and adds the human
// feature if it is missing. Returns hints if the file was updated.
func ensureHumanFeature(w io.Writer, fw claude.FileWriter) ([]string, error) {
	doc, err := devcontainer.LoadDocument(fw, devcontainerPath)
	if err != nil {
		return nil, err
	}
	if err := doc.AddFeature(humanFeatureKey); err != nil {
		return nil, err
	}
	if !doc.Changed() {
		_, _ = fmt.Fprintln(w, "Keeping existing devcontainer config (human feature already present).")
		return nil, nil
	}
	if err := doc.Save(); err != nil {
		return nil, err
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

// proxyAddrPrelude prepares HUMAN_PROXY_ADDR for the container's redirect
// script. The daemon hands containers it starts an address already resolved to
// an IP, so this leaves it untouched and nothing reads /etc/hosts at all. It
// only falls back to resolving the hostname on the standalone `devcontainer up`
// path, where nothing injects a resolved address and iptables would otherwise
// be handed a name it flatly refuses (nf_tables rejects a hostname in
// --to-destination). NR==1 is load-bearing in that fallback: a host mapped
// twice makes getent print two lines, and the address would carry a newline.
func proxyAddrPrelude() string {
	return fmt.Sprintf(
		`case "$HUMAN_PROXY_ADDR" in ''|*[!0-9.:]*) HUMAN_PROXY_ADDR="$(getent hosts %s | awk 'NR==1{print $1}'):%d";; esac; export HUMAN_PROXY_ADDR; `,
		daemon.DockerHost, daemon.DefaultProxyPort)
}

// goModRequirement is the Go version this project's go.mod demands, "" when it
// is not a Go project. The feature pin is written FROM it so the image cannot
// ship a Go the project already refuses — the relationship CI expresses as
// `go-version-file: go.mod` and the feature has no option for (SC-5879).
func goModRequirement(fw claude.FileWriter) string {
	data, err := fw.ReadFile("go.mod")
	if err != nil {
		return ""
	}
	return gotoolchain.GoDirective(data)
}

func buildDevcontainerConfig(proxy, intercept bool, stacks []StackType, caPresent bool, goVersion string) devcontainerConfig {
	featureOpts := map[string]any{}
	if proxy {
		featureOpts["proxy"] = true
	}

	features := map[string]any{
		nodeFeatureKey:   map[string]any{"version": "22"},
		humanFeatureKey:  featureOpts,
		claudeFeatureKey: map[string]any{},
	}
	for _, stack := range stacks {
		if stack.Fixed {
			continue // already added with pinned options above
		}
		opts := map[string]any{}
		// An unpinned go feature floats free of go.mod; pinning it to go.mod's
		// own requirement is what keeps a bumped go.mod from outrunning the
		// image (SC-5879). Left unpinned when there is no go.mod to read.
		if stack.FeatureKey == goFeatureKey && goVersion != "" {
			opts["version"] = goVersion
		}
		features[stack.FeatureKey] = opts
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

	cfg.CapAdd, cfg.Mounts, cfg.PostStartCommand = bootstrapFor(proxy, intercept, caPresent, stacks, cfg.RemoteEnv)
	return cfg
}

// bootstrapFor assembles the container's postStartCommand chain, plus the
// capabilities and mounts that only the intercepting variant needs.
//
// The toolchain check goes LAST, deliberately. Earlier in the chain a Go
// version mismatch would short-circuit the proxy redirect and CA trust, and the
// container would then present as a certificate failure at the model API —
// an infrastructure fault misattributed, which is what SC-4819 fixed. Last, it
// costs nothing and is the final line of the container's start output.
func bootstrapFor(proxy, intercept, caPresent bool, stacks []StackType, remoteEnv map[string]string) (capAdd, mounts []string, postStart string) {
	switch {
	case proxy && intercept:
		capAdd = []string{"NET_ADMIN"}
		if caPresent {
			// The CA bind-mount and its consumer (NODE_EXTRA_CA_CERTS +
			// update-ca-certificates) share one condition: the cert only has
			// meaning when MITM intercept is on. Mounting it for no-proxy or
			// proxy-without-intercept left a stray, unused certificate in the
			// container. caPresent still guards it — emitting the mount for a
			// missing source makes Docker fabricate an empty directory, and
			// Node's PEM parse then fails on every run.
			mounts = []string{"source=${localEnv:HOME}/.human/ca.crt,target=/home/vscode/.human/ca.crt,type=bind,readonly"}
			remoteEnv["NODE_EXTRA_CA_CERTS"] = "/home/vscode/.human/ca.crt"
			postStart = proxyAddrPrelude() + "sudo -E human-proxy-setup && sudo cp /home/vscode/.human/ca.crt /usr/local/share/ca-certificates/human-proxy.crt && sudo update-ca-certificates && human install --agent claude && human chrome-bridge"
		} else {
			postStart = proxyAddrPrelude() + "sudo -E human-proxy-setup && human install --agent claude && human chrome-bridge"
		}
	case proxy:
		capAdd = []string{"NET_ADMIN"}
		postStart = proxyAddrPrelude() + "sudo -E human-proxy-setup && human install --agent claude && human chrome-bridge"
	default:
		postStart = "human install --agent claude && human chrome-bridge"
	}
	if lsp := lspInstallCmd(stacks); lsp != "" {
		postStart += " && " + lsp
	}
	if hasGoStack(stacks) {
		postStart += " && " + toolchainCheckCmd
	}
	return capAdd, mounts, postStart
}

// toolchainCheckCmd names the mismatch at container start instead of letting
// every go invocation in the run rediscover it (SC-5879).
const toolchainCheckCmd = "human doctor toolchain"

func hasGoStack(stacks []StackType) bool {
	for _, s := range stacks {
		if s.FeatureKey == goFeatureKey {
			return true
		}
	}
	return false
}

// stackInstallCmd is the bootstrap command each language stack needs in the
// container. Package level because the generated ALLOWLIST is derived from the
// same table (egress_hosts.go): a command whose hosts nobody allowed is a
// container that cannot run its own bootstrap (SC-5879).
var stackInstallCmd = map[string]string{
	goFeatureKey:                              "go install golang.org/x/tools/gopls@latest",
	"ghcr.io/devcontainers/features/rust:1":   "rustup component add rust-analyzer",
	"ghcr.io/devcontainers/features/python:1": "npm install -g pyright",
	"ghcr.io/devcontainers/features/ruby:1":   "gem install solargraph",
	"ghcr.io/devcontainers/features/php:1":    "npm install -g intelephense",
}

// lspInstallCmd returns a shell command that installs LSP server binaries
// for the selected language stacks. Returns "" when no stacks have an LSP.
func lspInstallCmd(stacks []StackType) string {
	var cmds []string
	for _, stack := range stacks {
		if cmd, ok := stackInstallCmd[stack.FeatureKey]; ok {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return ""
	}
	return strings.Join(cmds, " && ")
}
