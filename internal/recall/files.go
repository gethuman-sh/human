package recall

import (
	"regexp"
	"sort"
	"strings"
)

// filePathRe matches a source path as it appears in prose: at least one slash
// and a short extension. Requiring the slash is what keeps ordinary sentences
// out — "e.g." and "v1.2" have no directory, and a bare "main.go" is too weak a
// signal to tie two tickets together.
var filePathRe = regexp.MustCompile(`[A-Za-z0-9_.\-/]+/[A-Za-z0-9_.\-]+\.[A-Za-z]{1,5}\b`)

// maxIndexedPaths bounds what one plan contributes. A plan that names half the
// tree says nothing useful about overlap, and the cap stops a pathological
// document from dominating the table.
const maxIndexedPaths = 64

// ExtractFilePaths returns the distinct source paths a plan names, in
// deterministic order.
//
// This is the signal that makes overlap a FACT rather than a keyword guess. The
// two tickets that were implemented twice — SC-1996 and SC-2042 — share no
// title or description words at all; what they share is that both plans name
// internal/daemon/board_transition.go. Full-text search cannot connect them,
// because the tokenizer splits that path into "internal", "daemon", "board",
// "transition", "go" — words common enough to match half the backlog. An exact
// path lookup connects them immediately.
func ExtractFilePaths(text string) []string {
	if text == "" {
		return nil
	}
	seen := make(map[string]struct{})
	for _, m := range filePathRe.FindAllString(text, -1) {
		p := strings.Trim(m, "./-")
		if p == "" || !strings.Contains(p, "/") {
			continue
		}
		seen[p] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) > maxIndexedPaths {
		out = out[:maxIndexedPaths]
	}
	return out
}
