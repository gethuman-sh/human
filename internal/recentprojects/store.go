// Package recentprojects persists the desktop app's most-recently-opened
// project list — up to 10 directories, most recent first — so the Projects
// Overview screen and the launch-time auto-load decision survive an app
// restart. Pure local workspace state, like internal/ideaspace: never a
// tracker label, comment, or status.
package recentprojects

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
)

// MaxEntries caps the recent-projects list at the size the Projects
// Overview screen renders ("up to the 10 most recently opened projects").
const MaxEntries = 10

// Entry is one recently opened project.
type Entry struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

type fileFormat struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

const currentVersion = 1

// Store reads and writes the recent-projects file, most-recent-first.
type Store struct {
	mu   sync.Mutex
	path string
}

// DefaultPath returns the recent-projects file location, matching the
// ~/.human convention (falling back to ./.human when no home is available).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "recentprojects.json")
	}
	return filepath.Join(home, ".human", "recentprojects.json")
}

// NewStore creates a store persisting to path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// List returns the saved entries, most-recent-first, dropping (and
// persisting the removal of) any whose directory no longer holds a
// .humanconfig file — self-healing, so a deleted/moved checkout silently
// falls off the list instead of dead-ending the Overview screen's "Open".
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.read()
	live := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if config.HasConfigFile(e.Dir) {
			live = append(live, e)
		}
	}
	if len(live) != len(entries) {
		_ = s.write(live)
	}
	return live
}

// Touch records dir (named name) as the most-recently-opened project: moves
// it to the front if already present (matched by absolute path), inserts it
// otherwise, and truncates to MaxEntries.
func (s *Store) Touch(dir, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	abs, err := filepath.Abs(dir)
	if err != nil {
		return errors.WrapWithDetails(err, "resolving project directory", "dir", dir)
	}

	entries := s.read()
	filtered := make([]Entry, 0, len(entries)+1)
	filtered = append(filtered, Entry{Name: name, Dir: abs})
	for _, e := range entries {
		if e.Dir == abs {
			continue
		}
		filtered = append(filtered, e)
	}
	if len(filtered) > MaxEntries {
		filtered = filtered[:MaxEntries]
	}
	return s.write(filtered)
}

// read loads the file tolerantly; callers hold s.mu.
func (s *Store) read() []Entry {
	data, err := os.ReadFile(s.path) // #nosec G304 -- path fixed at construction
	if err != nil {
		return nil
	}
	var f fileFormat
	if json.Unmarshal(data, &f) != nil || f.Version != currentVersion {
		return nil
	}
	return f.Entries
}

// write persists atomically via temp+rename; callers hold s.mu.
func (s *Store) write(entries []Entry) error {
	data, err := json.Marshal(fileFormat{Version: currentVersion, Entries: entries})
	if err != nil {
		return errors.WrapWithDetails(err, "marshal recent projects", "path", s.path)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return errors.WrapWithDetails(err, "create recent projects dir", "path", s.path)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return errors.WrapWithDetails(err, "write recent projects", "path", tmp)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return errors.WrapWithDetails(err, "rename recent projects", "path", s.path)
	}
	return nil
}
