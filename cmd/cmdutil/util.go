package cmdutil

import (
	"encoding/json"
	"io"
	"strings"
)

// PrintJSON encodes v as indented JSON to w.
func PrintJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// TruncateRunes limits s to at most maxRunes runes, appending "..." when it
// had to cut. It counts runes rather than bytes so a multi-byte character is
// never split mid-encoding (which would emit a replacement glyph).
func TruncateRunes(s string, maxRunes int) string {
	if maxRunes < 0 {
		maxRunes = 0
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "..."
}

// SplitIDs splits a comma-separated string into trimmed, non-empty parts.
func SplitIDs(ids string) []string {
	if ids == "" {
		return nil
	}
	parts := strings.Split(ids, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
