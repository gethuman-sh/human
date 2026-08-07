package claude

import (
	"path/filepath"
	"strings"
)

// AgentTranscriptRoot returns the transcript root of the agent container bound
// to projectDir. Agent containers keep their Claude state in the project rather
// than the operator's home — internal/devcontainer/manager.go:331 binds
// <projectDir>/.devcontainer/claude to the container's ~/.claude — so the spend
// of every agent run is written here and nowhere else.
//
// Only the projects/ subtree is named: the directory above it also holds
// .credentials.json, .claude.json, history.jsonl and todos/, none of which this
// tool has any business opening.
func AgentTranscriptRoot(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	return filepath.Join(projectDir, ".devcontainer", "claude", "projects")
}

// TranscriptRoots enumerates every Claude transcript root on this host: the
// operator's own ~/.claude/projects plus the agent root of each registered
// project. It exists so that no reader has to rebuild one hardcoded path and
// thereby see only half the machine's spend (SC-3581).
//
// Roots are resolved and de-duplicated because the scan has no per-message
// dedupe (jsonlLine carries no message id), so a tree reachable twice would be
// counted twice — turning an understated number into an overstated one. A root
// that does not exist is still returned: the walkers treat a missing tree as
// empty, so absence needs no special case here.
func TranscriptRoots(projectDirs []string) []string {
	candidates := make([]string, 0, len(projectDirs)+1)
	if host, err := ClaudeProjectsRoot(); err == nil {
		candidates = append(candidates, host)
	}
	for _, dir := range projectDirs {
		if root := AgentTranscriptRoot(dir); root != "" {
			candidates = append(candidates, root)
		}
	}
	return dedupeRoots(candidates)
}

// resolveRoot canonicalises a path for comparison, and incidentally makes it
// walkable: filepath.Walk does not follow symlinks and lstats the root itself,
// so a root whose final component is a symlink to a directory would otherwise
// be walked as a single non-directory entry and yield nothing.
//
// EvalSymlinks fails on a path that does not exist yet — a project whose
// container has never run — so the absolute form is kept rather than dropping a
// root that may fill in later.
func resolveRoot(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// dedupeRoots removes roots that would be walked twice: exact duplicates (the
// same project registered under two spellings) and roots nested inside another
// root (a checkout living under the operator's own tree), since the walk is
// recursive.
//
// Candidate order is preserved deliberately. Costs are float64 and float
// addition is not associative, so folding roots in a map-iteration order would
// make the totals — and the cost-descending model sort — vary between runs on
// identical input. The map decides membership only; the slice decides order.
func dedupeRoots(candidates []string) []string {
	resolved := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		r := resolveRoot(c)
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		resolved = append(resolved, r)
	}

	out := make([]string, 0, len(resolved))
	for _, r := range resolved {
		if hasAncestorIn(r, resolved) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// hasAncestorIn reports whether any other root in set strictly contains path.
func hasAncestorIn(path string, set []string) bool {
	for _, other := range set {
		if other != path && pathWithin(path, other) {
			return true
		}
	}
	return false
}

// pathWithin reports whether path lies strictly under prefix, matching on
// component boundaries so "/a/bc" is not treated as living under "/a/b".
func pathWithin(path, prefix string) bool {
	if path == prefix {
		return false
	}
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(path, prefix)
}
