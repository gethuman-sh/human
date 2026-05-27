package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
)

// ProjectEntry holds the loaded config context for one registered project directory.
type ProjectEntry struct {
	Name string // from .humanconfig project: field, or directory basename
	Dir  string // absolute path to project directory
}

// EnvLookup returns a per-project scoped environment variable lookup function.
// It implements a 4-level precedence chain for each key:
//
//  1. HUMAN_{PROJECT}_{KEY} — per-project override (e.g. HUMAN_INFRA_GITHUB_WORK_TOKEN)
//  2. {KEY} via os.LookupEnv — global fallback (e.g. GITHUB_WORK_TOKEN)
//
// The caller (ApplyEnvOverrides) constructs keys like PREFIX_SUFFIX and
// PREFIX_INSTANCE_SUFFIX. This lookup prepends HUMAN_{PROJECT}_ and checks
// that first, falling back to os.LookupEnv for the original key.
func (p ProjectEntry) EnvLookup() config.EnvLookup {
	prefix := "HUMAN_" + strings.ToUpper(p.Name) + "_"
	return func(key string) (string, bool) {
		// Per-project scoped: HUMAN_{PROJECT}_{KEY}
		if v, ok := os.LookupEnv(prefix + key); ok {
			return v, true
		}
		// Global fallback: {KEY}
		return os.LookupEnv(key)
	}
}

// ProjectRegistry maps project directories to their config entries.
// It is created at daemon startup and is read-only thereafter (no mutex needed).
type ProjectRegistry struct {
	entries []ProjectEntry
}

// NewProjectRegistry creates a registry from a list of project directories.
// Each directory must exist and contain a readable .humanconfig.
// If .humanconfig lacks a project: field, the directory basename is used as the name.
func NewProjectRegistry(dirs []string) (*ProjectRegistry, error) {
	entries := make([]ProjectEntry, 0, len(dirs))
	for _, dir := range dirs {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return nil, errors.WrapWithDetails(err, "resolving project directory", "dir", dir)
		}
		name := config.ReadProjectName(absDir)
		if name == "" {
			name = filepath.Base(absDir)
		}
		entries = append(entries, ProjectEntry{
			Name: name,
			Dir:  absDir,
		})
	}

	// Sort by directory path length descending so Resolve picks the longest prefix first.
	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].Dir) > len(entries[j].Dir)
	})

	return &ProjectRegistry{entries: entries}, nil
}

// Resolve finds the ProjectEntry whose Dir is a prefix of the given cwd.
// Returns (entry, true) on match, (zero, false) if no match.
// When multiple entries match (nested dirs), the longest prefix wins
// because entries are sorted by path length descending.
func (r *ProjectRegistry) Resolve(cwd string) (ProjectEntry, bool) {
	if cwd == "" {
		// No cwd provided — fall back to single-project if available.
		if len(r.entries) == 1 {
			return r.entries[0], true
		}
		return ProjectEntry{}, false
	}

	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return ProjectEntry{}, false
	}

	for _, e := range r.entries {
		if pathHasPrefix(absCwd, e.Dir) {
			return e, true
		}
	}
	return ProjectEntry{}, false
}

// Entries returns all registered project entries.
func (r *ProjectRegistry) Entries() []ProjectEntry {
	return r.entries
}

// Single returns true if there is exactly one registered project (backward compat mode).
func (r *ProjectRegistry) Single() bool {
	return len(r.entries) == 1
}

// pathHasPrefix reports whether path starts with prefix as a directory boundary.
// For example, /home/user/project matches /home/user/project and /home/user/project/sub,
// but not /home/user/project-extra.
func pathHasPrefix(path, prefix string) bool {
	if path == prefix {
		return true
	}
	// Ensure prefix ends with separator for proper boundary matching.
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(path, prefix)
}
