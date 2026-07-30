package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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

// KeyOrigin binds a PM issue key to the project directory the daemon fetched it
// from. SetOrigins consumes a fresh slice each board fetch.
type KeyOrigin struct {
	Key string
	Dir string
}

// ProjectRegistry maps project directories to their config entries.
// entries is built once at daemon startup and is read-only thereafter. origins
// is a mutable per-fetch index (guarded by mu) recording which project each PM
// issue key was last seen in, so board-action closures can route a request by
// its key instead of defaulting to a fixed project (SC-1694).
type ProjectRegistry struct {
	entries []ProjectEntry

	mu      sync.RWMutex
	origins map[string]map[string]struct{}
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

// SetOrigins replaces the key origin index wholesale with the given slice,
// recorded from the most recent issue fetch. It is a full replace, not a
// merge — stale keys from a prior fetch (e.g. a ticket moved trackers, or a
// project was unregistered) must not linger and misroute a later action.
func (r *ProjectRegistry) SetOrigins(origins []KeyOrigin) {
	index := make(map[string]map[string]struct{}, len(origins))
	for _, o := range origins {
		dirs, ok := index[o.Key]
		if !ok {
			dirs = make(map[string]struct{}, 1)
			index[o.Key] = dirs
		}
		dirs[o.Dir] = struct{}{}
	}

	r.mu.Lock()
	r.origins = index
	r.mu.Unlock()
}

// EntryForKey resolves the ProjectEntry that owns the given PM issue key.
//
// In the common single-project setup it always resolves to the sole entry,
// even before any origin has been recorded, preserving existing behavior.
// In a multi-project setup it requires the key to have been recorded by
// SetOrigins from exactly one project; an unrecorded key (no fetch yet) or a
// key seen in more than one project (overlapping key spaces) is refused
// rather than defaulting to entries[0] — the bug this fixes (SC-1694).
func (r *ProjectRegistry) EntryForKey(key string) (ProjectEntry, error) {
	if len(r.entries) == 1 {
		return r.entries[0], nil
	}
	if len(r.entries) == 0 {
		return ProjectEntry{}, errors.WithDetails("no project registered")
	}

	r.mu.RLock()
	dirs, ok := r.origins[key]
	r.mu.RUnlock()

	if !ok || len(dirs) == 0 {
		return ProjectEntry{}, errors.WithDetails("could not determine which project owns this key; refresh the board and try again", "key", key)
	}
	if len(dirs) > 1 {
		return ProjectEntry{}, errors.WithDetails("key is ambiguous across multiple registered projects", "key", key)
	}

	var dir string
	for d := range dirs {
		dir = d
	}
	for _, e := range r.entries {
		if e.Dir == dir {
			return e, nil
		}
	}
	return ProjectEntry{}, errors.WithDetails("recorded project origin is no longer registered", "key", key, "dir", dir)
}

// SoleEntry resolves keyless project-wide actions (bug-create, ideation, board
// git probes, ...) that carry no card to route by. It resolves in the common
// single-project setup and errors in a multi-project setup rather than
// silently binding a fixed project (SC-1694) — correctly routing these
// requires a wire-protocol/frontend change, out of scope here.
func (r *ProjectRegistry) SoleEntry() (ProjectEntry, error) {
	if len(r.entries) != 1 {
		return ProjectEntry{}, errors.WithDetails("this action carries no card key and requires exactly one registered project", "registered", len(r.entries))
	}
	return r.entries[0], nil
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
