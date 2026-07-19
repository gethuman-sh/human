package tracker

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// CredSpec describes the credentials required for a tracker kind.
type CredSpec struct {
	Kind      string   // "jira", "github", etc.
	EnvPrefix string   // "JIRA", "GITHUB", etc.
	Required  []string // env var suffixes: ["KEY", "USER"] for Jira, ["TOKEN"] for GitHub
	Label     string   // Human-readable name
	HelpURL   string   // Where to generate tokens
}

// CredResult tells the caller what credentials are available.
type CredResult struct {
	Spec      CredSpec
	Available map[string]string // suffix → value, for env vars that are set
	Missing   []string          // suffixes that are not set
	Complete  bool              // true if all required vars are set
}

// CredSpecs maps tracker kinds to their credential requirements.
// This is exported so the cmd layer can populate it with provider-specific
// knowledge, keeping internal/tracker free of provider-specific data.
// The cmd/cmdutil package populates it at init time.
var CredSpecs = map[string]CredSpec{}

// CredSpecForKind returns the credential specification for a tracker kind.
func CredSpecForKind(kind string) (CredSpec, bool) {
	spec, ok := CredSpecs[kind]
	return spec, ok
}

// CheckCreds checks which credentials are available in the environment.
// It checks global env vars (e.g. JIRA_KEY) for each required suffix.
func CheckCreds(spec CredSpec) CredResult {
	return CheckCredsEnv(spec, os.Getenv)
}

// CheckCredsEnv is like CheckCreds but accepts a custom env lookup function.
func CheckCredsEnv(spec CredSpec, getenv func(string) string) CredResult {
	result := CredResult{
		Spec:      spec,
		Available: make(map[string]string),
		Complete:  true,
	}

	for _, suffix := range spec.Required {
		envName := spec.EnvPrefix + "_" + suffix
		val := getenv(envName)
		if val != "" {
			result.Available[suffix] = val
		} else {
			result.Missing = append(result.Missing, suffix)
			result.Complete = false
		}
	}

	return result
}

// TrackerStatus describes a configured tracker entry and its credential state.
type TrackerStatus struct {
	Name     string   // config entry name, e.g. "work", "amazingcto"
	Kind     string   // tracker kind, e.g. "linear", "jira"
	Label    string   // human-readable name, e.g. "Linear", "Jira"
	Working  bool     // true when all required credentials are present (and not vault refs)
	VaultRef bool     // true when credentials are vault references (e.g. 1pw://) — unverified
	Missing  []string // env var names for missing credentials (empty when Working)
	// Role is the role DECLARED in .humanconfig ("pm", "engineering", or empty).
	// Captured independently of credential resolution so topology divergence — a
	// tracker declared role: engineering whose token does not resolve — can be
	// detected even when the entry is not Working (SC-660 rule 7).
	Role string
}

// KindToSection maps tracker kinds to their .humanconfig YAML section names.
// This is the canonical mapping; use SectionToKind() for the inverse.
var KindToSection = map[string]string{
	"jira":        "jiras",
	"github":      "githubs",
	"gitlab":      "gitlabs",
	"linear":      "linears",
	"azuredevops": "azuredevops",
	"shortcut":    "shortcuts",
}

// sectionToKind is the inverse of KindToSection, derived at init time.
var sectionToKind map[string]string

func init() {
	sectionToKind = make(map[string]string, len(KindToSection))
	for kind, section := range KindToSection {
		sectionToKind[section] = kind
	}
}

// SectionToKind returns the mapping of .humanconfig section names to tracker kinds.
func SectionToKind() map[string]string {
	return sectionToKind
}

// diagnoseEntry holds the config fields needed to check credentials.
type diagnoseEntry struct {
	Name   string `mapstructure:"name"`
	Key    string `mapstructure:"key"`
	User   string `mapstructure:"user"`
	Token  string `mapstructure:"token"`
	Secret string `mapstructure:"secret"` // #nosec G117 -- config field name, not an actual secret value
	Role   string `mapstructure:"role"`
}

func (e diagnoseEntry) fieldValue(suffix string) string {
	switch suffix {
	case "KEY":
		return e.Key
	case "USER":
		return e.User
	case "TOKEN":
		return e.Token
	case "SECRET":
		return e.Secret
	default:
		return ""
	}
}

// DiagnoseTrackers reads all configured tracker entries and checks whether
// their required credentials are present. unmarshal reads a YAML section,
// getenv looks up environment variables.
func DiagnoseTrackers(dir string, unmarshal func(dir, section string, target any) error, getenv func(string) string) []TrackerStatus {
	var result []TrackerStatus
	for section, kind := range sectionToKind {
		var entries []diagnoseEntry
		// A parse error previously collapsed to zero results, so a
		// broken humanconfig presented as "no trackers configured"
		// rather than surfacing the underlying YAML issue. Emit a
		// synthetic Working=false status so the user can see and fix
		// the problem.
		if err := unmarshal(dir, section, &entries); err != nil {
			spec, ok := CredSpecs[kind]
			label := kind
			if ok {
				label = spec.Label
			}
			result = append(result, TrackerStatus{
				Name:    section,
				Kind:    kind,
				Label:   label,
				Working: false,
				Missing: []string{"config parse error: " + err.Error()},
			})
			continue
		}

		spec, ok := CredSpecs[kind]
		if !ok {
			continue
		}

		for _, entry := range entries {
			missing, vaultRef := diagnoseMissing(spec, entry, getenv)
			result = append(result, TrackerStatus{
				Name:     entry.Name,
				Kind:     kind,
				Label:    spec.Label,
				Working:  len(missing) == 0 && !vaultRef,
				VaultRef: vaultRef,
				Missing:  missing,
				Role:     entry.Role,
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// diagnoseMissing returns env var names for credentials not provided by
// the config file, per-instance env vars, or global env vars.
// The second return value is true if any credential is a vault reference
// (e.g. 1pw://) that cannot be verified without vault resolution.
func diagnoseMissing(spec CredSpec, entry diagnoseEntry, getenv func(string) string) ([]string, bool) {
	var missing []string
	vaultRef := false
	for _, suffix := range spec.Required {
		val := entry.fieldValue(suffix)
		if val != "" {
			if strings.HasPrefix(val, "1pw://") {
				vaultRef = true
			}
			continue
		}
		if entry.Name != "" {
			instEnv := spec.EnvPrefix + "_" + strings.ToUpper(entry.Name) + "_" + suffix
			if getenv(instEnv) != "" {
				continue
			}
		}
		globalEnv := spec.EnvPrefix + "_" + suffix
		if getenv(globalEnv) != "" {
			continue
		}
		missing = append(missing, spec.EnvPrefix+"_"+suffix)
	}
	return missing, vaultRef
}

// FormatMissingCreds returns a user-friendly message about which env vars to set.
func FormatMissingCreds(result CredResult, parsed *ParsedURL) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Cannot fetch ticket from %s at %s\n", result.Spec.Label, parsed.BaseURL)
	fmt.Fprintln(&b, "  Set these environment variables:")
	for _, suffix := range result.Missing {
		envName := result.Spec.EnvPrefix + "_" + suffix
		fmt.Fprintf(&b, "    export %s=your-%s\n", envName, strings.ToLower(suffix))
	}
	if result.Spec.HelpURL != "" {
		fmt.Fprintf(&b, "\n  Generate credentials at: %s\n", result.Spec.HelpURL)
	}

	return b.String()
}
