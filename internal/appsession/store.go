// Package appsession tracks which daemon process the desktop app currently
// considers itself attached to and managing — purely so a future launch can
// tell a daemon orphaned by a crashed/killed app session (SC-3015's
// launch-time cleanup prompt) apart from one a user runs standalone on
// purpose. Local-only, like ideaspace/recentprojects/boardprefs: never a
// tracker label, comment, or status.
package appsession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Marker records the last-known pairing of this app's own process with the
// daemon process it observed as reachable.
type Marker struct {
	AppPID    int `json:"appPID"`
	DaemonPID int `json:"daemonPID"`
}

type fileFormat struct {
	Version int    `json:"version"`
	Marker  Marker `json:"marker"`
}

const currentVersion = 1

// Store reads and writes the session marker file.
type Store struct {
	mu   sync.Mutex
	path string
}

// DefaultPath returns the session marker file location, matching the
// ~/.human convention (falling back to ./.human when no home is available).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "appsession.json")
	}
	return filepath.Join(home, ".human", "appsession.json")
}

// NewStore creates a store persisting to path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Mark records appPID as the process currently managing the daemon at
// daemonPID. Called on every daemon-reachable status poll while the app runs,
// so the marker self-heals across a daemon handover (new PID, same logical
// daemon) within one poll interval.
func (s *Store) Mark(appPID, daemonPID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(Marker{AppPID: appPID, DaemonPID: daemonPID})
}

// Read returns the last-written marker, or (_, false) when none exists or the
// file is unreadable/corrupt — tolerated the same way recentprojects and
// boardprefs treat a bad file: a missing signal, never a surfaced error.
func (s *Store) Read() (Marker, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path) // #nosec G304 -- path fixed at construction
	if err != nil {
		return Marker{}, false
	}
	var f fileFormat
	if json.Unmarshal(data, &f) != nil || f.Version != currentVersion {
		return Marker{}, false
	}
	return f.Marker, true
}

// Clear removes the marker (best-effort) after a deliberate, graceful daemon
// stop, so a cleanly stopped daemon is never later reported as orphaned.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// write persists atomically via temp+rename; callers hold s.mu.
func (s *Store) write(m Marker) error {
	data, err := json.Marshal(fileFormat{Version: currentVersion, Marker: m})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// IsOrphaned reports whether marker names the SAME daemon process (by PID)
// that is reachable right now, while the app process that wrote the marker is
// no longer alive — the crash/force-quit/kill-9 signature this ticket must
// detect. Any mismatch (no marker, a daemon restarted since with a different
// PID, or the recording app still alive) resolves to false: an unmanaged or
// still-legitimately-managed daemon must never be flagged as orphaned.
func IsOrphaned(marker Marker, present bool, daemonPID int, alive func(pid int) bool) bool {
	if !present || marker.DaemonPID == 0 || marker.DaemonPID != daemonPID {
		return false
	}
	return !alive(marker.AppPID)
}
