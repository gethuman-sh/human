// Package boardcache persists the desktop workflow board's last-known ticket
// snapshot so a cold open (including after an app restart) paints instantly from
// the previous view instead of blanking on the live tracker fetch. It is the
// durable half of the board's stale-while-revalidate load: written after every
// successful full board fetch, read back before the next fetch lands. Pure local
// UI acceleration — never a tracker read/write — and scoped to one project at a
// time so switching projects can never paint another project's board.
package boardcache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/gethuman-sh/human/errors"
)

const currentVersion = 1

// fileFormat is the on-disk shape. Version guards against a later schema change
// misreading an old file; Project scopes the single stored snapshot to the
// project it was captured from.
type fileFormat struct {
	Version  int             `json:"version"`
	Project  string          `json:"project"`
	Snapshot json.RawMessage `json:"snapshot"`
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

// Load returns the cached snapshot for project, and true, only when the stored
// snapshot belongs to that same project. A missing, corrupt, future-versioned,
// different-project, or empty file yields (nil, false) — a miss the caller
// treats as "no cache; fall back to the live-fetch spinner".
func (s *Store) Load(project string) (json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.read()
	if !ok || f.Project != project || len(f.Snapshot) == 0 {
		return nil, false
	}
	return f.Snapshot, true
}

// Save replaces the cache with snapshot for project. The cache holds exactly one
// project's board (the app serves one project at a time), so a save for a new
// project overwrites the previous one rather than accumulating.
func (s *Store) Save(project string, snapshot json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(fileFormat{Version: currentVersion, Project: project, Snapshot: snapshot})
}

// read loads the file tolerantly; callers hold s.mu.
func (s *Store) read() (fileFormat, bool) {
	data, err := os.ReadFile(s.path) // #nosec G304 — path fixed at construction
	if err != nil {
		return fileFormat{}, false
	}
	var f fileFormat
	if json.Unmarshal(data, &f) != nil || f.Version != currentVersion {
		return fileFormat{}, false
	}
	return f, true
}

// write persists atomically via temp+rename so a crash mid-write can never leave
// a half-written file that would silently corrupt the snapshot; callers hold s.mu.
func (s *Store) write(f fileFormat) error {
	f.Version = currentVersion
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
