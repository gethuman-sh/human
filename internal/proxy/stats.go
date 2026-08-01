package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Stats holds proxy runtime metrics written by the daemon and read by its clients.
type Stats struct {
	ActiveConns int64 `json:"active_conns"`
}

// StatsPath returns the default path for the proxy stats file (~/.human/proxy-stats.json).
func StatsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "proxy-stats.json")
	}
	return filepath.Join(home, ".human", "proxy-stats.json")
}

// WriteStats atomically writes stats to path (write tmp + rename).
func WriteStats(path string, s Stats) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadStats reads proxy stats from path. Returns zero stats on any error.
func ReadStats(path string) Stats {
	data, err := os.ReadFile(path) // #nosec G304 — path built from StatsPath()
	if err != nil {
		return Stats{}
	}
	var s Stats
	if json.Unmarshal(data, &s) != nil {
		return Stats{}
	}
	return s
}

// RemoveStats removes the proxy stats file (best-effort).
func RemoveStats(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + ".tmp")
}
