// Package devcontainer manages devcontainer lifecycle: parsing devcontainer.json,
// building images, creating/starting/stopping containers, and executing commands.
package devcontainer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
)

// HumanConfig holds devcontainer settings from .humanconfig.
type HumanConfig struct {
	ConfigDir string `yaml:"configdir"`
}

// LoadHumanConfig reads the devcontainer section from .humanconfig.
func LoadHumanConfig(dir string) (*HumanConfig, error) {
	var cfg HumanConfig
	if err := config.UnmarshalSection(dir, "devcontainer", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// DevcontainerConfig represents a parsed devcontainer.json.
// Supports the subset of the spec needed for image-based and Dockerfile-based configs.
type DevcontainerConfig struct {
	Name                 string            `json:"name,omitempty"`
	Image                string            `json:"image,omitempty"`
	Build                *BuildConfig      `json:"build,omitempty"`
	DockerFile           string            `json:"dockerFile,omitempty"`
	Features             map[string]any    `json:"features,omitempty"`
	Mounts               []any             `json:"mounts,omitempty"`
	RunArgs              []string          `json:"runArgs,omitempty"`
	ForwardPorts         []any             `json:"forwardPorts,omitempty"`
	RemoteEnv            map[string]string `json:"remoteEnv,omitempty"`
	ContainerEnv         map[string]string `json:"containerEnv,omitempty"`
	RemoteUser           string            `json:"remoteUser,omitempty"`
	ContainerUser        string            `json:"containerUser,omitempty"`
	WorkspaceFolder      string            `json:"workspaceFolder,omitempty"`
	CapAdd               []string          `json:"capAdd,omitempty"`
	SecurityOpt          []string          `json:"securityOpt,omitempty"`
	Privileged           bool              `json:"privileged,omitempty"`
	OverrideCommand      *bool             `json:"overrideCommand,omitempty"`
	InitializeCommand    any               `json:"initializeCommand,omitempty"`
	OnCreateCommand      any               `json:"onCreateCommand,omitempty"`
	UpdateContentCommand any               `json:"updateContentCommand,omitempty"`
	PostCreateCommand    any               `json:"postCreateCommand,omitempty"`
	PostStartCommand     any               `json:"postStartCommand,omitempty"`
	PostAttachCommand    any               `json:"postAttachCommand,omitempty"`
}

// BuildConfig holds Dockerfile build configuration.
type BuildConfig struct {
	Dockerfile string            `json:"dockerfile,omitempty"`
	Context    string            `json:"context,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
	Target     string            `json:"target,omitempty"`
	CacheFrom  []string          `json:"cacheFrom,omitempty"`
}

// FindConfig locates the devcontainer.json file for a project directory.
// Search order: .devcontainer/devcontainer.json, then .devcontainer.json.
func FindConfig(projectDir string) (string, error) {
	candidates := []string{
		filepath.Join(projectDir, ".devcontainer", "devcontainer.json"),
		filepath.Join(projectDir, ".devcontainer.json"),
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", errors.WithDetails("devcontainer.json not found",
		"project_dir", projectDir,
		"searched", strings.Join(candidates, ", "))
}

// ParseConfig parses a devcontainer.json file, handling JSONC (comments).
func ParseConfig(data []byte) (*DevcontainerConfig, error) {
	stripped := StripJSONC(data)
	var cfg DevcontainerConfig
	if err := json.Unmarshal(stripped, &cfg); err != nil {
		return nil, errors.WrapWithDetails(err, "parsing devcontainer.json")
	}
	return &cfg, nil
}

// ReadConfig finds and parses the devcontainer.json for a project directory.
func ReadConfig(projectDir string) (*DevcontainerConfig, error) {
	path, err := FindConfig(projectDir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path derived from FindConfig
	if err != nil {
		return nil, errors.WrapWithDetails(err, "reading devcontainer.json", "path", path)
	}
	return ParseConfig(data)
}

// ResolveVariables replaces devcontainer.json variable placeholders in string
// fields. Supported: ${localEnv:VAR}, ${localWorkspaceFolder},
// ${localWorkspaceFolderBasename}.
func ResolveVariables(cfg *DevcontainerConfig, projectDir string) *DevcontainerConfig {
	absDir, _ := filepath.Abs(projectDir)
	resolve := func(s string) string {
		s = replaceLocalEnv(s)
		s = strings.ReplaceAll(s, "${localWorkspaceFolder}", absDir)
		s = strings.ReplaceAll(s, "${localWorkspaceFolderBasename}", filepath.Base(absDir))
		return s
	}

	// Resolve string map fields.
	cfg.RemoteEnv = resolveMap(cfg.RemoteEnv, resolve)
	cfg.ContainerEnv = resolveMap(cfg.ContainerEnv, resolve)
	cfg.WorkspaceFolder = resolve(cfg.WorkspaceFolder)
	cfg.Image = resolve(cfg.Image)

	// Resolve mounts (string entries only).
	for i, m := range cfg.Mounts {
		if s, ok := m.(string); ok {
			cfg.Mounts[i] = resolve(s)
		}
	}

	return cfg
}

// replaceLocalEnv replaces all ${localEnv:VAR} and ${localEnv:VAR:default}
// placeholders with their values from the host environment.
func replaceLocalEnv(s string) string {
	for {
		start := strings.Index(s, "${localEnv:")
		if start < 0 {
			return s
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			return s
		}
		end += start

		// Extract variable name and optional default.
		inner := s[start+len("${localEnv:") : end]
		varName, defaultVal, hasDefault := strings.Cut(inner, ":")

		val, ok := os.LookupEnv(varName)
		if !ok && hasDefault {
			val = defaultVal
		}

		s = s[:start] + val + s[end+1:]
	}
}

func resolveMap(m map[string]string, resolve func(string) string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = resolve(v)
	}
	return out
}

// StripJSONC removes // line comments and /* */ block comments from JSONC input,
// preserving content inside JSON string literals.
func StripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		if data[i] == '"' {
			i = copyJSONString(data, i, &out)
			continue
		}
		if i+1 < len(data) && data[i] == '/' && data[i+1] == '/' {
			i = skipLineComment(data, i)
			continue
		}
		if i+1 < len(data) && data[i] == '/' && data[i+1] == '*' {
			i = skipBlockComment(data, i)
			continue
		}
		out = append(out, data[i])
		i++
	}
	return out
}

// copyJSONString copies a JSON string literal (including quotes) to out,
// handling escape sequences. Returns the index after the closing quote.
func copyJSONString(data []byte, start int, out *[]byte) int {
	*out = append(*out, data[start])
	i := start + 1
	for i < len(data) {
		*out = append(*out, data[i])
		if data[i] == '\\' && i+1 < len(data) {
			i++
			*out = append(*out, data[i])
			i++
			continue
		}
		if data[i] == '"' {
			return i + 1
		}
		i++
	}
	return i
}

// skipLineComment advances past a // comment. Returns the index of the newline.
func skipLineComment(data []byte, start int) int {
	i := start + 2
	for i < len(data) && data[i] != '\n' {
		i++
	}
	return i
}

// skipBlockComment advances past a /* */ comment. Returns the index after */.
func skipBlockComment(data []byte, start int) int {
	i := start + 2
	for i+1 < len(data) {
		if data[i] == '*' && data[i+1] == '/' {
			return i + 2
		}
		i++
	}
	return len(data) // unterminated comment: consume everything
}
