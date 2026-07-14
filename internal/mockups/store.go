// Package mockups persists the ticket→mockup-set link: which locally
// generated mockup set (mockups/<slug>/ in the project) belongs to which PM
// ticket. The link lives in the project — next to the mockup files it points
// at — deliberately never as a label, comment, or status on the ticket, so
// generating mockups leaves no trace on the tracker and the link travels (and
// dies) with the clone.
package mockups

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// Entry links one ticket to its mockup set. Created marks when generation was
// launched: an entry without a finished set is "in progress" while young and
// treated as stale (absent) once old, so a crashed agent can never
// permanently block regeneration.
type Entry struct {
	Slug    string    `json:"slug"`
	Created time.Time `json:"created"`
}

// fileFormat is the on-disk shape. Version exists so a later change (e.g.
// multiple sets per ticket) can migrate instead of misreading.
type fileFormat struct {
	Version int              `json:"version"`
	Mocks   map[string]Entry `json:"mocks"`
}

const currentVersion = 1

// PathIn returns the mapping file location inside a project directory,
// following the .human/ convention for durable local artifacts.
func PathIn(projectDir string) string {
	return filepath.Join(projectDir, ".human", "mockups.json")
}

// Store reads and writes the (ticket key → mockup set) mapping file.
type Store struct {
	// mu serializes read-modify-write cycles; daemon routes and Wails binding
	// calls can run concurrently and a lost update would silently drop a link.
	mu   sync.Mutex
	path string
}

// NewStore creates a store persisting to path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// All returns the saved ticket→entry map. A missing, corrupt, or
// future-versioned file yields an empty map — an absent link simply means "no
// mockups yet", so there is nothing to fail loudly about.
func (s *Store) All() map[string]Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// Set persists the mockup link for one ticket key.
func (s *Store) Set(key string, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mocks := s.read()
	mocks[key] = e
	return s.write(mocks)
}

// Delete drops the link for one ticket key — used to roll back a mapping
// whose agent launch failed. A missing file or key is a no-op so rollback
// never creates state.
func (s *Store) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.path); err != nil {
		return nil
	}
	mocks := s.read()
	if _, ok := mocks[key]; !ok {
		return nil
	}
	delete(mocks, key)
	return s.write(mocks)
}

// SlugFor derives the mockup set directory slug from a ticket key: lowercase
// with every non-alphanumeric run collapsed to a single hyphen ("SC-123" →
// "sc-123"). Deterministic so the launcher knows the output directory before
// the generating agent has produced anything.
func SlugFor(pmKey string) string {
	var b strings.Builder
	pending := false
	for _, r := range strings.ToLower(pmKey) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pending && b.Len() > 0 {
				b.WriteByte('-')
			}
			pending = false
			b.WriteRune(r)
		default:
			pending = true
		}
	}
	return b.String()
}

// read loads the file tolerantly; callers hold s.mu.
func (s *Store) read() map[string]Entry {
	data, err := os.ReadFile(s.path) // #nosec G304 — path fixed at construction
	if err != nil {
		return map[string]Entry{}
	}
	var f fileFormat
	if json.Unmarshal(data, &f) != nil || f.Version != currentVersion || f.Mocks == nil {
		return map[string]Entry{}
	}
	return f.Mocks
}

// write persists atomically via temp+rename so a crash mid-write can never
// leave a half-written file that would silently drop every link; callers
// hold s.mu.
func (s *Store) write(mocks map[string]Entry) error {
	data, err := json.Marshal(fileFormat{Version: currentVersion, Mocks: mocks})
	if err != nil {
		return errors.WrapWithDetails(err, "marshal mockup links", "path", s.path)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return errors.WrapWithDetails(err, "create mockup links dir", "path", s.path)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return errors.WrapWithDetails(err, "write mockup links", "path", tmp)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return errors.WrapWithDetails(err, "rename mockup links", "path", s.path)
	}
	return nil
}
