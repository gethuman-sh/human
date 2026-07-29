// Package boardcache persists the desktop workflow board's last-known ticket
// snapshot so a cold open (including after an app restart) paints instantly from
// the previous view instead of blanking on the live tracker fetch. It is the
// durable half of the board's stale-while-revalidate load: written after every
// successful full board fetch, read back before the next fetch lands. Pure local
// UI acceleration — never a tracker read/write — and keeps a per-project snapshot
// map so each project retains its own instant-paint snapshot instead of evicting
// another project's on save.
package boardcache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/gethuman-sh/human/errors"
)

const currentVersion = 2

// fileFormat is the on-disk shape. Snapshots maps a project directory to its
// last board snapshot so switching projects never evicts another's cache. The
// Project/Snapshot fields are the legacy v1 shape, read only to migrate.
type fileFormat struct {
	Version   int                        `json:"version"`
	Snapshots map[string]json.RawMessage `json:"snapshots"`
	Project   string                     `json:"project,omitempty"`  // legacy v1
	Snapshot  json.RawMessage            `json:"snapshot,omitempty"` // legacy v1
}

// Store reads and writes the board-snapshot cache file.
type Store struct {
	// mu serializes read-modify-write cycles; Wails binding calls can run
	// concurrently and a torn write would corrupt the snapshot.
	mu   sync.Mutex
	path string
}

// DefaultPath returns the cache file location, matching the ~/.human convention
// (falling back to ./.human when no home is available).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "boardcache.json")
	}
	return filepath.Join(home, ".human", "boardcache.json")
}

// NewStore creates a store persisting to path.
func NewStore(path string) *Store { return &Store{path: path} }

// Load returns the cached snapshot for project, and true, only when a snapshot
// is stored for that project. A missing, corrupt, future-versioned,
// different-project, or empty file yields (nil, false) — a miss the caller
// treats as "no cache; fall back to the live-fetch spinner".
func (s *Store) Load(project string) (json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.read()
	if !ok {
		return nil, false
	}
	snap, ok := f.Snapshots[project]
	if !ok || len(snap) == 0 {
		return nil, false
	}
	return snap, true
}

// Save stores snapshot as project's cache entry, accumulating alongside any
// other project's entry rather than evicting it — the app may serve more than
// one project across its lifetime, and switching back to a project must still
// hit the cache instead of a cold spinner.
func (s *Store) Save(project string, snapshot json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.read()
	if !ok {
		f = fileFormat{Version: currentVersion, Snapshots: map[string]json.RawMessage{}}
	}
	if f.Snapshots == nil {
		f.Snapshots = map[string]json.RawMessage{}
	}
	f.Snapshots[project] = snapshot
	return s.write(f)
}

// read loads the file tolerantly and migrates a legacy v1 single-slot file into
// the project-keyed map in memory; callers hold s.mu. Migration is persisted
// only on the next Save, keeping the read path side-effect-free.
func (s *Store) read() (fileFormat, bool) {
	data, err := os.ReadFile(s.path) // #nosec G304 — path fixed at construction
	if err != nil {
		return fileFormat{}, false
	}
	var f fileFormat
	if json.Unmarshal(data, &f) != nil {
		return fileFormat{}, false
	}
	switch f.Version {
	case currentVersion:
		if f.Snapshots == nil {
			f.Snapshots = map[string]json.RawMessage{}
		}
		return f, true
	case 1:
		m := fileFormat{Version: currentVersion, Snapshots: map[string]json.RawMessage{}}
		if f.Project != "" && len(f.Snapshot) > 0 {
			m.Snapshots[f.Project] = f.Snapshot
		}
		return m, true
	default:
		return fileFormat{}, false
	}
}

// write persists atomically via temp+rename so a crash mid-write can never leave
// a half-written file that would silently corrupt the snapshot; callers hold s.mu.
func (s *Store) write(f fileFormat) error {
	f.Version = currentVersion
	f.Project = "" // never persist legacy fields
	f.Snapshot = nil
	data, err := json.Marshal(f)
	if err != nil {
		return errors.WrapWithDetails(err, "marshal board cache", "path", s.path)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return errors.WrapWithDetails(err, "create board cache dir", "path", s.path)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return errors.WrapWithDetails(err, "write board cache", "path", tmp)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return errors.WrapWithDetails(err, "rename board cache", "path", s.path)
	}
	return nil
}
