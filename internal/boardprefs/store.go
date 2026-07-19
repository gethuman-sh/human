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

// fileFormat is the on-disk shape. Version exists so a later change (e.g.
// scoping preferences per tracker) can migrate instead of misreading.
type fileFormat struct {
	Version int                 `json:"version"`
	Columns map[string][]string `json:"columns"`
	Hidden  []string            `json:"hidden"`
}

const currentVersion = 1

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

// Snapshot returns the saved preferences. A missing, corrupt, or
// future-versioned file yields empty preferences — no saved order simply
// means fetch order, and nothing hidden.
func (s *Store) Snapshot() Prefs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return prefsOf(s.read())
}

// SetOrder replaces the hand-sorted key list for one queue.
func (s *Store) SetOrder(queue string, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f := s.read()
	if f.Columns == nil {
		f.Columns = map[string][]string{}
	}
	f.Columns[queue] = keys
	return s.write(f)
}

// SetHidden parks or restores one ticket.
func (s *Store) SetHidden(key string, hidden bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f := s.read()
	kept := make([]string, 0, len(f.Hidden)+1)
	for _, k := range f.Hidden {
		if k != key {
			kept = append(kept, k)
		}
	}
	if hidden {
		kept = append(kept, key)
	}
	f.Hidden = kept
	return s.write(f)
}

// PruneExcept drops preferences for tickets not in keys — closed or vanished
// tickets no longer need an order slot or a hidden flag. A missing file is a
// no-op so pruning never creates state.
func (s *Store) PruneExcept(keys map[string]struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.path); err != nil {
		return nil
	}
	f := s.read()
	changed := false

	for queue, order := range f.Columns {
		kept := make([]string, 0, len(order))
		for _, k := range order {
			if _, ok := keys[k]; ok {
				kept = append(kept, k)
			}
		}
		if len(kept) != len(order) {
			f.Columns[queue] = kept
			changed = true
		}
	}
	keptHidden := make([]string, 0, len(f.Hidden))
	for _, k := range f.Hidden {
		if _, ok := keys[k]; ok {
			keptHidden = append(keptHidden, k)
		}
	}
	if len(keptHidden) != len(f.Hidden) {
		f.Hidden = keptHidden
		changed = true
	}

	if !changed {
		return nil
	}
	return s.write(f)
}

func prefsOf(f fileFormat) Prefs {
	p := Prefs{Columns: f.Columns, Hidden: make(map[string]struct{}, len(f.Hidden))}
	if p.Columns == nil {
		p.Columns = map[string][]string{}
	}
	for _, k := range f.Hidden {
		p.Hidden[k] = struct{}{}
	}
	return p
}

// read loads the file tolerantly; callers hold s.mu.
func (s *Store) read() fileFormat {
	empty := fileFormat{Version: currentVersion, Columns: map[string][]string{}}
	data, err := os.ReadFile(s.path) // #nosec G304 — path fixed at construction
	if err != nil {
		return empty
	}
	var f fileFormat
	if json.Unmarshal(data, &f) != nil || f.Version != currentVersion {
		return empty
	}
	if f.Columns == nil {
		f.Columns = map[string][]string{}
	}
	return f
}

// write persists atomically via temp+rename so a crash mid-write can never
// leave a half-written file that would silently reset every preference;
// callers hold s.mu.
func (s *Store) write(f fileFormat) error {
	f.Version = currentVersion
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
