package devcontainer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// Status represents the lifecycle state of a managed devcontainer.
type Status string

const (
	StatusRunning  Status = "running"
	StatusStopped  Status = "stopped"
	StatusCreating Status = "creating"
	StatusFailed   Status = "failed"
)

// Meta holds persisted metadata for a managed devcontainer.
type Meta struct {
	Name          string    `json:"name"`
	ProjectDir    string    `json:"project_dir"`
	ContainerID   string    `json:"container_id"`
	ContainerName string    `json:"container_name"`
	ImageID       string    `json:"image_id"`
	ImageName     string    `json:"image_name"`
	Status        Status    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	StartedAt     time.Time `json:"started_at,omitzero"`
	StoppedAt     time.Time `json:"stopped_at,omitzero"`
	WorkspaceDir  string    `json:"workspace_dir"`
	RemoteUser    string    `json:"remote_user"`
	DaemonAddr    string    `json:"daemon_addr,omitempty"`
	ConfigHash    string    `json:"config_hash"`
}

// DevcontainersDir returns the directory where devcontainer metadata is stored.
func DevcontainersDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "devcontainers")
	}
	return filepath.Join(home, ".human", "devcontainers")
}

// MetaPath returns the file path for a devcontainer's metadata JSON.
func MetaPath(name string) string {
	return filepath.Join(DevcontainersDir(), name+".json")
}

// WriteMeta persists devcontainer metadata to disk.
func WriteMeta(m Meta) error {
	dir := DevcontainersDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.WrapWithDetails(err, "creating devcontainers directory", "dir", dir)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling devcontainer metadata", "name", m.Name)
	}
	data = append(data, '\n')
	path := MetaPath(m.Name)                                // #nosec G703 -- name is sanitized by SanitizeName
	if err := os.WriteFile(path, data, 0o600); err != nil { // #nosec G306 G703 -- path from MetaPath, name is sanitized
		return errors.WrapWithDetails(err, "writing devcontainer metadata", "path", path)
	}
	return nil
}

// ReadMeta loads devcontainer metadata from disk.
func ReadMeta(name string) (Meta, error) {
	path := MetaPath(name)
	data, err := os.ReadFile(path) // #nosec G304 G703 -- path derived from sanitized name
	if err != nil {
		return Meta{}, errors.WrapWithDetails(err, "reading devcontainer metadata", "path", path)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, errors.WrapWithDetails(err, "parsing devcontainer metadata", "path", path)
	}
	return m, nil
}

// ListMetas returns metadata for all managed devcontainers.
func ListMetas() ([]Meta, error) {
	dir := DevcontainersDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.WrapWithDetails(err, "listing devcontainers directory", "dir", dir)
	}
	var metas []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		m, err := ReadMeta(name)
		if err != nil {
			continue
		}
		metas = append(metas, m)
	}
	return metas, nil
}

// DeleteMeta removes the devcontainer metadata file from disk.
func DeleteMeta(name string) error {
	path := MetaPath(name)
	if err := os.Remove(path); err != nil { // #nosec G703 -- path from MetaPath, name is sanitized
		return errors.WrapWithDetails(err, "deleting devcontainer metadata", "path", path)
	}
	return nil
}
