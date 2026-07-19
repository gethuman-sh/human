//go:build linux

package fusefs

import (
	"bytes"
	"strings"
)

const redactedValue = "***"

// sensitiveKeywords triggers redaction when a key contains any of these (case-insensitive).
var sensitiveKeywords = []string{
	"KEY", "SECRET", "TOKEN", "PASSWORD", "PASSWD", "PWD",
	"CREDENTIAL", "AUTH", "PRIVATE", "CERTIFICATE", "CERT",
}

// sensitiveValuePrefixes triggers redaction when a value starts with any of these.
var sensitiveValuePrefixes = []string{
	"sk-", "sk_live_", "sk_test_",
	"ghp_", "gho_",
	"glpat-",
	"xoxb-", "xoxp-",
}

// safeKeys are preserved as-is regardless of other rules.
var safeKeys = map[string]bool{
	"NODE_ENV":    true,
	"ENV":         true,
	"ENVIRONMENT": true,
	"DEBUG":       true,
	"LOG_LEVEL":   true,
	"PORT":        true,
	"HOST":        true,
	"HOSTNAME":    true,
}

// safeValues are preserved as-is regardless of other rules.
var safeValues = map[string]bool{
	"true":        true,
	"false":       true,
	"0":           true,
	"1":           true,
	"development": true,
	"production":  true,
	"staging":     true,
	"test":        true,
}

// RedactEnv takes the raw content of an env-style KEY=VALUE file and returns
// a copy with secret values replaced by "***". Comments and blank lines are
// preserved. Safe keys and safe values are kept as-is.
//
// Splitting on '\n' directly (rather than via bufio.Scanner) avoids the 64 KiB
// per-line cap that would silently drop realistic env values like base64-
// encoded certificates and long JWT chains — Scanner returned bufio.ErrTooLong
// on such lines and the caller never saw the truncation.
func RedactEnv(content []byte) []byte {
	var buf bytes.Buffer
	rest := content
	first := true
	for len(rest) > 0 {
		var line []byte
		if idx := bytes.IndexByte(rest, '\n'); idx >= 0 {
			line = rest[:idx]
			rest = rest[idx+1:]
		} else {
			line = rest
			rest = nil
		}
		// Match bufio.ScanLines: a trailing '\r' from a CRLF terminator is
		// stripped so classification does not see it as part of the value.
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if !first {
			buf.WriteByte('\n')
		}
		first = false
		buf.WriteString(redactLine(string(line)))
	}
	// Preserve trailing newline if present.
	if len(content) > 0 && content[len(content)-1] == '\n' {
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// redactLine processes a single line from an env file.
func redactLine(line string) string {
	trimmed := strings.TrimSpace(line)

	// Blank lines and comments pass through.
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return line
	}

	// Lines without '=' pass through (e.g. export-only statements).
	before, after, ok := strings.Cut(trimmed, "=")
	if !ok {
		return line
	}

	key := strings.TrimSpace(before)
	value := strings.TrimSpace(after)

	// Strip optional quotes from value for classification.
	unquoted := unquoteValue(value)

	if isSensitive(key, unquoted) {
		return key + "=" + redactedValue
	}
	return line
}

// unquoteValue removes surrounding single or double quotes from a value.
func unquoteValue(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// isSensitive returns true if the key/value pair should be redacted.
func isSensitive(key, value string) bool {
	upperKey := strings.ToUpper(key)

	// Safe keys are never redacted.
	if safeKeys[upperKey] {
		return false
	}

	// Safe values are never redacted.
	lowerValue := strings.ToLower(value)
	if safeValues[lowerValue] {
		return false
	}

	// Check key against sensitive keywords.
	for _, kw := range sensitiveKeywords {
		if strings.Contains(upperKey, kw) {
			return true
		}
	}

	// Check value against sensitive prefixes.
	for _, prefix := range sensitiveValuePrefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}

	// Check for embedded credentials in URLs.
	if strings.Contains(value, "://") && strings.Contains(value, "@") {
		// Look for ://user:pass@ pattern.
		afterScheme := value[strings.Index(value, "://")+3:]
		if strings.Contains(afterScheme, ":") && strings.Contains(afterScheme, "@") {
			atIdx := strings.Index(afterScheme, "@")
			userPass := afterScheme[:atIdx]
			if strings.Contains(userPass, ":") {
				return true
			}
		}
	}

	return false
}
