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

// fileFormat is the on-disk shape. Version exists so a later change (e.g.
// scoping assignments per tracker) can migrate instead of misreading.
type fileFormat struct {
	Version int            `json:"version"`
	Ideas   map[string]int `json:"ideas"`
}

const currentVersion = 1

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

// Assignments returns the saved ticket→column map. A missing, corrupt, or
// future-versioned file yields an empty map — an absent assignment simply
// means "leftmost column", so there is nothing to fail loudly about.
func (s *Store) Assignments() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// Set persists the column for one ticket key. Out-of-range columns are
// clamped rather than rejected: a drop landed, honoring it approximately
// beats losing the gesture.
func (s *Store) Set(key string, col int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ideas := s.read()
	ideas[key] = clamp(col)
	return s.write(ideas)
}

// PruneExcept drops assignments for tickets not in keys — ideas that were
// promoted (idea label removed) or closed no longer need a placement. A
// missing file is a no-op so pruning never creates state.
func (s *Store) PruneExcept(keys map[string]struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.path); err != nil {
		return nil
	}
	ideas := s.read()
	pruned := make(map[string]int, len(ideas))
	for key, col := range ideas {
		if _, ok := keys[key]; ok {
			pruned[key] = col
		}
	}
	if len(pruned) == len(ideas) {
		return nil
	}
	return s.write(pruned)
}

// read loads the file tolerantly; callers hold s.mu.
func (s *Store) read() map[string]int {
	data, err := os.ReadFile(s.path) // #nosec G304 — path fixed at construction
	if err != nil {
		return map[string]int{}
	}
	var f fileFormat
	if json.Unmarshal(data, &f) != nil || f.Version != currentVersion || f.Ideas == nil {
		return map[string]int{}
	}
	for key, col := range f.Ideas {
		f.Ideas[key] = clamp(col)
	}
	return f.Ideas
}

// write persists atomically via temp+rename so a crash mid-write can never
// leave a half-written file that would silently reset every placement;
// callers hold s.mu.
func (s *Store) write(ideas map[string]int) error {
	data, err := json.Marshal(fileFormat{Version: currentVersion, Ideas: ideas})
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
