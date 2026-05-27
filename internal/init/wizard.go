package init

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"text/template"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/logo"
)

// WizardStep is a self-contained phase of the init wizard.
// Run returns optional follow-up hints (e.g. install instructions) to be
// printed after the wizard finishes, plus any error.
type WizardStep interface {
	Name() string
	Run(w io.Writer, fw claude.FileWriter) (hints []string, err error)
}

// Prompter is a composite interface embedding all step-specific prompter interfaces.
type Prompter interface {
	ServicesPrompter
	VaultPrompter
	DevcontainerPrompter
	ClaudeMigratePrompter
	LspPrompter
	AgentInstallPrompter
}

// ServiceType describes a configurable service with its YAML key, defaults, and env var pattern.
type ServiceType struct {
	Label       string   // display name, e.g. "Jira"
	ConfigKey   string   // YAML top-level key, e.g. "jiras"
	DefaultURL  string   // empty means user must provide it
	URLRequired bool     // if true and DefaultURL is empty, prompt for URL
	ExtraFields []string // additional fields beyond name+description, e.g. "user", "org"
	EnvVars     []string // env var suffixes, e.g. ["KEY"] → JIRA_{NAME}_KEY
	EnvPrefix   string   // e.g. "JIRA"
}

// ServiceRegistry returns all available services.
func ServiceRegistry() []ServiceType {
	return []ServiceType{
		{
			Label: "Jira", ConfigKey: "jiras",
			URLRequired: true, ExtraFields: []string{"user"},
			EnvVars: []string{"KEY"}, EnvPrefix: "JIRA",
		},
		{
			Label: "GitHub", ConfigKey: "githubs",
			DefaultURL: "https://api.github.com",
			EnvVars:    []string{"TOKEN"}, EnvPrefix: "GITHUB",
		},
		{
			Label: "GitLab", ConfigKey: "gitlabs",
			DefaultURL: "https://gitlab.com",
			EnvVars:    []string{"TOKEN"}, EnvPrefix: "GITLAB",
		},
		{
			Label: "Linear", ConfigKey: "linears",
			DefaultURL: "https://api.linear.app",
			EnvVars:    []string{"TOKEN"}, EnvPrefix: "LINEAR",
		},
		{
			Label: "Azure DevOps", ConfigKey: "azuredevops",
			DefaultURL:  "https://dev.azure.com",
			ExtraFields: []string{"org"},
			EnvVars:     []string{"TOKEN"}, EnvPrefix: "AZURE",
		},
		{
			Label: "Shortcut", ConfigKey: "shortcuts",
			DefaultURL: "https://api.app.shortcut.com",
			EnvVars:    []string{"TOKEN"}, EnvPrefix: "SHORTCUT",
		},
		{
			Label: "Notion", ConfigKey: "notions",
			DefaultURL: "https://api.notion.com",
			EnvVars:    []string{"TOKEN"}, EnvPrefix: "NOTION",
		},
		{
			Label: "Figma", ConfigKey: "figmas",
			DefaultURL: "https://api.figma.com",
			EnvVars:    []string{"TOKEN"}, EnvPrefix: "FIGMA",
		},
		{
			Label: "Amplitude", ConfigKey: "amplitudes",
			DefaultURL:  "https://amplitude.com",
			URLRequired: true,
			EnvVars:     []string{"KEY", "SECRET"}, EnvPrefix: "AMPLITUDE",
		},
	}
}

// serviceInstance holds the values collected for one service instance.
type serviceInstance struct {
	Service ServiceType
	Values  map[string]string
}

// EnvVarName returns the env var name for a given service instance and suffix.
func EnvVarName(prefix, instanceName, suffix string) string {
	return prefix + "_" + strings.ToUpper(instanceName) + "_" + suffix
}

// configData groups instances by config key for template rendering.
type configData struct {
	Sections []configSection
}

type configSection struct {
	ConfigKey string
	Instances []configInstance
}

type configInstance struct {
	Name        string
	URL         string
	User        string
	Org         string
	Description string
	EnvComments []string
}

// GenerateConfig produces the YAML config from collected service instances.
func GenerateConfig(instances []serviceInstance) (string, error) {
	// Group by config key, preserving order.
	sectionOrder := make([]string, 0)
	sectionMap := make(map[string][]configInstance)

	for _, inst := range instances {
		key := inst.Service.ConfigKey
		if _, exists := sectionMap[key]; !exists {
			sectionOrder = append(sectionOrder, key)
		}

		ci := configInstance{
			Name:        inst.Values["name"],
			URL:         inst.Values["url"],
			User:        inst.Values["user"],
			Org:         inst.Values["org"],
			Description: inst.Values["description"],
		}

		for _, suffix := range inst.Service.EnvVars {
			envName := EnvVarName(inst.Service.EnvPrefix, ci.Name, suffix)
			ci.EnvComments = append(ci.EnvComments, fmt.Sprintf("export %s=your-%s", envName, strings.ToLower(suffix)))
		}

		sectionMap[key] = append(sectionMap[key], ci)
	}

	data := configData{}
	for _, key := range sectionOrder {
		data.Sections = append(data.Sections, configSection{
			ConfigKey: key,
			Instances: sectionMap[key],
		})
	}

	tmpl, err := template.New("config").Funcs(template.FuncMap{
		"yamlSafe": yamlSafeString,
	}).Parse(configTemplate)
	if err != nil {
		return "", errors.WrapWithDetails(err, "parsing config template")
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", errors.WrapWithDetails(err, "executing config template")
	}

	return buf.String(), nil
}

const configTemplate = `{{- range $i, $section := .Sections }}{{ if $i }}
{{ end }}{{ $section.ConfigKey }}:
{{- range $section.Instances }}
  - name: {{ .Name | yamlSafe }}
{{- if .URL }}
    url: {{ .URL | yamlSafe }}
{{- end }}
{{- if .User }}
    user: {{ .User | yamlSafe }}
{{- end }}
{{- if .Org }}
    org: {{ .Org | yamlSafe }}
{{- end }}
{{- if .Description }}
    description: {{ .Description | yamlSafe }}
{{- end }}
{{- range .EnvComments }}
    # {{ . }}
{{- end }}
{{- end }}
{{- end }}
`

// yamlSafeString returns s quoted if it contains characters that could
// break YAML structure or inject fields, otherwise returns it as-is.
func yamlSafeString(s string) string {
	if strings.ContainsAny(s, "\"\n\r\\{}[]|>&*!") {
		escaped := strings.ReplaceAll(s, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		escaped = strings.ReplaceAll(escaped, "\n", `\n`)
		escaped = strings.ReplaceAll(escaped, "\r", `\r`)
		return `"` + escaped + `"`
	}
	return s
}

// WizardState holds data shared between wizard steps.
// It is passed by pointer so that earlier steps can populate fields
// that later steps consume.
type WizardState struct {
	SelectedStacks   []StackType
	ProxyEnabled     bool
	InterceptEnabled bool
	VaultProvider    string // e.g. "1password", empty if none
	VaultAccount     string // e.g. 1Password account name
}

// DefaultProxyDomains provides a sensible allowlist for new projects.
var DefaultProxyDomains = []string{
	"*.github.com",
	"api.openai.com",
	"claude.ai",
	"*.googleapis.com",
	"*.githubusercontent.com",
	"*.claude.com",
	"*.anthropic.com",
}

// StackToLspBinary maps devcontainer feature keys to LSP binary names
// from LspRegistry. Used to auto-select LSP plugins when language
// stacks are already chosen in the devcontainer step.
func StackToLspBinary() map[string]string {
	return map[string]string{
		"ghcr.io/devcontainers/features/go:1":     "gopls",
		"ghcr.io/devcontainers/features/rust:1":   "rust-analyzer",
		"ghcr.io/devcontainers/features/python:1": "pyright-langserver",
		"ghcr.io/devcontainers/features/java:1":   "jdtls",
		"ghcr.io/devcontainers/features/ruby:1":   "solargraph",
		"ghcr.io/devcontainers/features/dotnet:2": "OmniSharp",
		"ghcr.io/devcontainers/features/php:1":    "intelephense",
		"ghcr.io/devcontainers/features/node:1":   "vtsls",
	}
}

// StackType describes a language stack that can be added as a devcontainer feature.
type StackType struct {
	Label      string // display name, e.g. "Go"
	FeatureKey string // devcontainer feature reference, e.g. "ghcr.io/devcontainers/features/go:1"
	Fixed      bool   // always included, cannot be deselected (shown as pre-checked)
}

// StackRegistry returns all available language stacks.
func StackRegistry() []StackType {
	return []StackType{
		{Label: "Node.js 22 (required by Claude Code)", FeatureKey: "ghcr.io/devcontainers/features/node:1", Fixed: true},
		{Label: "Go", FeatureKey: "ghcr.io/devcontainers/features/go:1"},
		{Label: "Rust", FeatureKey: "ghcr.io/devcontainers/features/rust:1"},
		{Label: "Python", FeatureKey: "ghcr.io/devcontainers/features/python:1"},
		{Label: "Java", FeatureKey: "ghcr.io/devcontainers/features/java:1"},
		{Label: "Ruby", FeatureKey: "ghcr.io/devcontainers/features/ruby:1"},
		{Label: ".NET", FeatureKey: "ghcr.io/devcontainers/features/dotnet:2"},
		{Label: "PHP", FeatureKey: "ghcr.io/devcontainers/features/php:1"},
	}
}

// configPath is the filename written by the services step.
const configPath = ".humanconfig.yaml"

// RunInit orchestrates the init wizard by running each step in sequence.
// Follow-up hints from all steps are printed after the final "Done!" line.
func RunInit(w io.Writer, steps []WizardStep, fw claude.FileWriter) error {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, logo.Render())
	_, _ = fmt.Fprintln(w)

	var allHints []string
	for _, step := range steps {
		hints, err := step.Run(w, fw)
		if err != nil {
			return errors.WrapWithDetails(err, "wizard step %s", "step", step.Name())
		}
		allHints = append(allHints, hints...)
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Done! Run 'human tracker list --table' to verify.")

	if len(allHints) > 0 {
		_, _ = fmt.Fprintln(w)
		for _, hint := range allHints {
			_, _ = fmt.Fprintln(w, hint)
		}
	}

	return nil
}
