// Package boardprefs persists the workflow board's per-user view preferences:
// the hand-sorted vertical order of cards within each queue column and the set
// of tickets the user hid from the board. Like the idea-space placement, this
// is pure local UI preference — deliberately a file on the user's machine and
// never a label, comment, or status on the ticket, so arranging or parking
// cards leaves no trace on the tracker and needs no tracker credentials.
package boardprefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/gethuman-sh/human/errors"
)

// projectPrefs is one project's view preferences.
type projectPrefs struct {
	Columns map[string][]string `json:"columns"`
	Hidden  []string            `json:"hidden"`
}

// fileFormat is the on-disk shape. Projects maps a project directory to its
// preferences. The Columns/Hidden fields are the legacy v1 shape, read only to
// migrate an existing single-project file into the active project's slot.
type fileFormat struct {
	Version  int                     `json:"version"`
	Projects map[string]projectPrefs `json:"projects"`
	Columns  map[string][]string     `json:"columns,omitempty"` // legacy v1
	Hidden   []string                `json:"hidden,omitempty"`  // legacy v1
}

const currentVersion = 2

// Prefs is one consistent snapshot of both preference kinds, taken under a
// single lock so a caller never sees an order from before a hide.
type Prefs struct {
	// Columns maps a queue id to its hand-sorted ticket keys, top first.
	// Cards absent from the list render after it in fetch order.
	Columns map[string][]string
	// Hidden holds the ticket keys the user parked off the board.
	Hidden map[string]struct{}
}

// Store reads and writes the preference file.
type Store struct {
	// mu serializes read-modify-write cycles; Wails binding calls can run
	// concurrently and a lost update would silently drop a preference.
	mu   sync.Mutex
	path string
}

// DefaultPath returns the preference file location, matching the ~/.human
// convention (falling back to ./.human when no home is available).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "boardprefs.json")
	}
	return filepath.Join(home, ".human", "boardprefs.json")
}

// NewStore creates a store persisting to path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Snapshot returns project's saved preferences. A missing, corrupt, or
// future-versioned file yields empty preferences — no saved order simply
// means fetch order, and nothing hidden.
func (s *Store) Snapshot(project string) Prefs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return prefsOf(s.read(project))
}

// SetOrder replaces the hand-sorted key list for one queue in project.
func (s *Store) SetOrder(project, queue string, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f := s.readAll(project)
	pp := f.Projects[project]
	if pp.Columns == nil {
		pp.Columns = map[string][]string{}
	}
	pp.Columns[queue] = keys
	f.Projects[project] = pp
	return s.write(f)
}

// SetHidden parks or restores one ticket in project.
func (s *Store) SetHidden(project, key string, hidden bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f := s.readAll(project)
	pp := f.Projects[project]
	kept := make([]string, 0, len(pp.Hidden)+1)
	for _, k := range pp.Hidden {
		if k != key {
			kept = append(kept, k)
		}
	}
	if hidden {
		kept = append(kept, key)
	}
	pp.Hidden = kept
	f.Projects[project] = pp
	return s.write(f)
}

// PruneExcept drops preferences for tickets not in keys — closed or vanished
// tickets no longer need an order slot or a hidden flag — scoped to project. A
// missing file is a no-op so pruning never creates state.
func (s *Store) PruneExcept(project string, keys map[string]struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.path); err != nil {
		return nil
	}
	f := s.readAll(project)
	pp := f.Projects[project]
	changed := false

	for queue, order := range pp.Columns {
		kept := make([]string, 0, len(order))
		for _, k := range order {
			if _, ok := keys[k]; ok {
				kept = append(kept, k)
			}
		}
		if len(kept) != len(order) {
			pp.Columns[queue] = kept
			changed = true
		}
	}
	keptHidden := make([]string, 0, len(pp.Hidden))
	for _, k := range pp.Hidden {
		if _, ok := keys[k]; ok {
			keptHidden = append(keptHidden, k)
		}
	}
	if len(keptHidden) != len(pp.Hidden) {
		pp.Hidden = keptHidden
		changed = true
	}

	if !changed {
		return nil
	}
	f.Projects[project] = pp
	return s.write(f)
}

func prefsOf(pp projectPrefs) Prefs {
	p := Prefs{Columns: pp.Columns, Hidden: make(map[string]struct{}, len(pp.Hidden))}
	if p.Columns == nil {
		p.Columns = map[string][]string{}
	}
	for _, k := range pp.Hidden {
		p.Hidden[k] = struct{}{}
	}
	return p
}

// read returns one project's prefs, folding a legacy v1 file into that project
// in memory (not persisted); callers hold s.mu.
func (s *Store) read(project string) projectPrefs {
	f := s.readAll(project)
	pp := f.Projects[project]
	if pp.Columns == nil {
		pp.Columns = map[string][]string{}
	}
	return pp
}

// readAll returns the full project-keyed file, migrating a legacy v1 single-
// project file into the requested project's slot; callers hold s.mu.
func (s *Store) readAll(project string) fileFormat {
	empty := fileFormat{Version: currentVersion, Projects: map[string]projectPrefs{}}
	data, err := os.ReadFile(s.path) // #nosec G304 — path fixed at construction
	if err != nil {
		return empty
	}
	var f fileFormat
	if json.Unmarshal(data, &f) != nil {
		return empty
	}
	switch f.Version {
	case currentVersion:
		if f.Projects == nil {
			f.Projects = map[string]projectPrefs{}
		}
		return f
	case 1:
		cols := f.Columns
		if cols == nil {
			cols = map[string][]string{}
		}
		return fileFormat{Version: currentVersion, Projects: map[string]projectPrefs{
			project: {Columns: cols, Hidden: f.Hidden},
		}}
	default:
		return empty
	}
}

// write persists atomically via temp+rename so a crash mid-write can never
// leave a half-written file that would silently reset every preference;
// callers hold s.mu.
func (s *Store) write(f fileFormat) error {
	f.Version = currentVersion
	f.Columns = nil // never persist legacy fields
	f.Hidden = nil
	data, err := json.Marshal(f)
	if err != nil {
		return errors.WrapWithDetails(err, "marshal board prefs", "path", s.path)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return errors.WrapWithDetails(err, "create board prefs dir", "path", s.path)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return errors.WrapWithDetails(err, "write board prefs", "path", tmp)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return errors.WrapWithDetails(err, "rename board prefs", "path", s.path)
	}
	return nil
}
