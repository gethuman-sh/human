// Package ideaspace persists the board's idea-space placement: which of the
// five loose→concrete sub-columns each idea ticket sits in. This is pure local
// UI preference — deliberately a file on the user's machine and never a label,
// comment, or status on the ticket, so sorting ideas leaves no trace on the
// tracker and needs no tracker credentials.
package ideaspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/gethuman-sh/human/errors"
)

// Columns is the width of the idea space: index 0 holds the loosest ideas,
// Columns-1 the most concrete.
const Columns = 5

// fileFormat is the on-disk shape. IdeasByProject maps a project directory to
// its (ticket key → column) map. Ideas is the legacy v1 shape, read only to
// migrate an existing single-project file into the active project's slot.
type fileFormat struct {
	Version        int                       `json:"version"`
	IdeasByProject map[string]map[string]int `json:"ideas_by_project"`
	Ideas          map[string]int            `json:"ideas,omitempty"` // legacy v1
}

const currentVersion = 2

// Store reads and writes the (ticket key → column index) assignment file.
type Store struct {
	// mu serializes read-modify-write cycles; Wails binding calls can run
	// concurrently and a lost update would silently drop a placement.
	mu   sync.Mutex
	path string
}

// DefaultPath returns the assignment file location, matching the ~/.human
// convention (falling back to ./.human when no home is available).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "ideaspace.json")
	}
	return filepath.Join(home, ".human", "ideaspace.json")
}

// NewStore creates a store persisting to path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Assignments returns project's saved ticket→column map. A missing, corrupt,
// or future-versioned file yields an empty map — an absent assignment simply
// means "leftmost column", so there is nothing to fail loudly about.
func (s *Store) Assignments(project string) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(project)
}

// Set persists the column for one ticket key in project. Out-of-range columns
// are clamped rather than rejected: a drop landed, honoring it approximately
// beats losing the gesture.
func (s *Store) Set(project, key string, col int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f := s.readAll(project)
	f.IdeasByProject[project][key] = clamp(col)
	return s.write(f)
}

// PruneExcept drops assignments for tickets not in keys — ideas that were
// promoted (idea label removed) or closed no longer need a placement — scoped
// to project. A missing file is a no-op so pruning never creates state.
func (s *Store) PruneExcept(project string, keys map[string]struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.path); err != nil {
		return nil
	}
	f := s.readAll(project)
	ideas := f.IdeasByProject[project]
	pruned := make(map[string]int, len(ideas))
	for key, col := range ideas {
		if _, ok := keys[key]; ok {
			pruned[key] = col
		}
	}
	if len(pruned) == len(ideas) {
		return nil
	}
	f.IdeasByProject[project] = pruned
	return s.write(f)
}

// read returns one project's clamped assignments, folding a legacy v1 file into
// that project in memory (not persisted); callers hold s.mu.
func (s *Store) read(project string) map[string]int {
	f := s.readAll(project)
	return f.IdeasByProject[project]
}

// readAll returns the full project-keyed file with every column clamped and the
// requested project's slot guaranteed non-nil, migrating a legacy v1 file into
// that project's slot; callers hold s.mu.
func (s *Store) readAll(project string) fileFormat {
	f := fileFormat{Version: currentVersion, IdeasByProject: map[string]map[string]int{}}
	data, err := os.ReadFile(s.path) // #nosec G304 — path fixed at construction
	if err == nil {
		var parsed fileFormat
		if json.Unmarshal(data, &parsed) == nil {
			switch parsed.Version {
			case currentVersion:
				if parsed.IdeasByProject != nil {
					f.IdeasByProject = parsed.IdeasByProject
				}
			case 1:
				if parsed.Ideas != nil {
					f.IdeasByProject[project] = parsed.Ideas
				}
			}
		}
	}
	if f.IdeasByProject[project] == nil {
		f.IdeasByProject[project] = map[string]int{}
	}
	for _, ideas := range f.IdeasByProject {
		for key, col := range ideas {
			ideas[key] = clamp(col)
		}
	}
	return f
}

// write persists atomically via temp+rename so a crash mid-write can never
// leave a half-written file that would silently reset every placement;
// callers hold s.mu.
func (s *Store) write(f fileFormat) error {
	f.Version = currentVersion
	f.Ideas = nil // never persist legacy fields
	data, err := json.Marshal(f)
	if err != nil {
		return errors.WrapWithDetails(err, "marshal idea space", "path", s.path)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return errors.WrapWithDetails(err, "create idea space dir", "path", s.path)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return errors.WrapWithDetails(err, "write idea space", "path", tmp)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return errors.WrapWithDetails(err, "rename idea space", "path", s.path)
	}
	return nil
}

func clamp(col int) int {
	if col < 0 {
		return 0
	}
	if col >= Columns {
		return Columns - 1
	}
	return col
}
