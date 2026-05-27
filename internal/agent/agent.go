// Package agent manages Claude Code agent lifecycle: spawning, stopping,
// listing, attaching, and resuming background tmux sessions in devcontainers.
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// ContainerPrefix is prepended to all managed agent container names.
const ContainerPrefix = "human-agent-"

// Status represents the lifecycle state of an agent.
type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusFailed  Status = "failed"
)

// Meta holds the persisted metadata for a single agent.
type Meta struct {
	Name          string    `json:"name"`
	ContainerID   string    `json:"container_id"`
	ContainerName string    `json:"container_name"`
	Cwd           string    `json:"cwd"`
	Prompt        string    `json:"prompt,omitempty"`
	Status        Status    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	StoppedAt     time.Time `json:"stopped_at,omitempty"`
	SkipPerms     bool      `json:"skip_perms,omitempty"`
	Model         string    `json:"model,omitempty"`
	ConfigDir     string    `json:"config_dir,omitempty"`
	ImageName     string    `json:"image_name,omitempty"`
	RemoteUser    string    `json:"remote_user,omitempty"`
}

// ContainerName returns the Docker container name for an agent.
func ContainerName(name string) string {
	return ContainerPrefix + name
}

// AgentsDir returns the directory where agent metadata is stored.
// Falls back to ./.human/agents/ when the home directory is unknown.
func AgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "agents")
	}
	return filepath.Join(home, ".human", "agents")
}

// MetaPath returns the file path for the given agent's metadata JSON.
func MetaPath(name string) string {
	return filepath.Join(AgentsDir(), name+".json")
}

// WriteMeta persists agent metadata to disk as indented JSON with restricted
// file permissions (0600).
func WriteMeta(m Meta) error {
	dir := AgentsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.WrapWithDetails(err, "creating agents directory", "dir", dir)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling agent metadata", "name", m.Name)
	}
	data = append(data, '\n')
	path := MetaPath(m.Name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return errors.WrapWithDetails(err, "writing agent metadata", "path", path)
	}
	return nil
}

// ReadMeta loads agent metadata from disk.
func ReadMeta(name string) (Meta, error) {
	path := MetaPath(name)
	data, err := os.ReadFile(path) // #nosec G304 -- path derived from agent name
	if err != nil {
		return Meta{}, errors.WrapWithDetails(err, "reading agent metadata", "path", path)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, errors.WrapWithDetails(err, "parsing agent metadata", "path", path)
	}
	return m, nil
}

// ListMetas returns metadata for all agents, sorted by creation time (newest first).
func ListMetas() ([]Meta, error) {
	dir := AgentsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.WrapWithDetails(err, "listing agents directory", "dir", dir)
	}
	var metas []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		m, err := ReadMeta(name)
		if err != nil {
			continue // skip corrupt files
		}
		metas = append(metas, m)
	}
	return metas, nil
}

// DeleteMeta removes the agent metadata file from disk.
func DeleteMeta(name string) error {
	path := MetaPath(name)
	if err := os.Remove(path); err != nil {
		return errors.WrapWithDetails(err, "deleting agent metadata", "path", path)
	}
	return nil
}

// FormatDuration returns a human-readable duration string.
// Examples: "2m", "1h30m", "3d12h".
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	if hours < 24 {
		mins := int(d.Minutes()) - hours*60
		if mins == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	days := hours / 24
	remainHours := hours % 24
	if remainHours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, remainHours)
}
