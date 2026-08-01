// Package redact scrubs secrets from text that leaves the host — tracker
// comments, persisted hook-event records, anything public relative to the
// daemon's environment. It is deliberately dependency-free (stdlib only) so
// both internal/agent and internal/daemon can share the exact same redaction
// terms without an import cycle.
package redact

import (
	"os"
	"regexp"
	"strings"
	"sync"
)

// secretShapeRes match well-known token formats regardless of where they came
// from. The redacted text lands somewhere public relative to the host — so
// anything token-shaped is scrubbed even if it is not one of ours.
var secretShapeRes = []*regexp.Regexp{
	regexp.MustCompile(`gh[opsru]_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`xox[a-z]-[A-Za-z0-9-]{8,}`),
	regexp.MustCompile(`lin_api_[A-Za-z0-9]{8,}`),
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`glpat-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
}

// isSecretEnvName marks env vars whose VALUES must never reach a tracker
// comment; it deliberately mirrors the <TRACKER>_<NAME>_TOKEN / _KEY
// convention the daemon documents. Matching whole underscore-segments (not
// substrings) keeps PATH out while catching GITHUB_PAT.
func isSecretEnvName(name string) bool {
	for seg := range strings.SplitSeq(strings.ToUpper(name), "_") {
		switch seg {
		case "TOKEN", "SECRET", "KEY", "PASSWORD", "PASSWD", "PAT", "CREDENTIAL", "CREDENTIALS", "APIKEY":
			return true
		}
	}
	return false
}

// minSecretEnvLen keeps trivially short values (e.g. KEY=1) out of the
// redaction list — replacing those would shred ordinary log text.
const minSecretEnvLen = 8

var (
	secretEnvOnce   sync.Once
	secretEnvValues []string
)

// secretEnvList caches the env-derived redaction list; the daemon's environ is
// stable for the process lifetime.
func secretEnvList() []string {
	secretEnvOnce.Do(func() { secretEnvValues = CollectSecretEnv(os.Environ()) })
	return secretEnvValues
}

// CollectSecretEnv extracts the values of secret-named env vars from environ.
func CollectSecretEnv(environ []string) []string {
	var vals []string
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || len(val) < minSecretEnvLen {
			continue
		}
		if isSecretEnvName(name) {
			vals = append(vals, val)
		}
	}
	return vals
}

// Text scrubs a string for a destination that is public relative to the host.
func Text(s string) string {
	return TextWith(s, secretEnvList())
}

// TextWith replaces token-shaped strings and the given secret values with
// [redacted] and de-identifies the home directory.
func TextWith(s string, secrets []string) string {
	for _, re := range secretShapeRes {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	for _, sec := range secrets {
		s = strings.ReplaceAll(s, sec, "[redacted]")
	}
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		s = strings.ReplaceAll(s, home, "~")
	}
	return s
}
