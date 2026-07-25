package devcontainer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadMeta(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	m := Meta{
		Name:          "test-dc",
		ProjectDir:    "/home/user/project",
		ContainerID:   "abc123",
		ContainerName: "human-dc-project",
		ImageID:       "sha256:def456",
		ImageName:     "human-dc-project:abc123abc123",
		Status:        StatusRunning,
		CreatedAt:     time.Now().Truncate(time.Second),
		WorkspaceDir:  "/workspaces/project",
		RemoteUser:    "vscode",
		ConfigHash:    "abc123abc123abc123abc123",
	}

	if err := WriteMeta(m); err != nil {
		t.Fatal(err)
	}

	// File should exist with restricted permissions.
	path := MetaPath("test-dc")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600", perm)
	}

	got, err := ReadMeta("test-dc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != m.Name {
		t.Errorf("Name = %q, want %q", got.Name, m.Name)
	}
	if got.ContainerID != m.ContainerID {
		t.Errorf("ContainerID = %q, want %q", got.ContainerID, m.ContainerID)
	}
	if got.Status != StatusRunning {
		t.Errorf("Status = %q, want %q", got.Status, StatusRunning)
	}
	if !got.CreatedAt.Equal(m.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, m.CreatedAt)
	}
}

func TestReadMeta_NotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_, err := ReadMeta("nonexistent")
	if err == nil {
		t.Error("expected error for missing metadata")
	}
}

func TestListMetas(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Write two entries.
	for _, name := range []string{"alpha", "beta"} {
		if err := WriteMeta(Meta{
			Name:      name,
			Status:    StatusRunning,
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	metas, err := ListMetas()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Errorf("got %d metas, want 2", len(metas))
	}
}

func TestListMetas_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	metas, err := ListMetas()
	if err != nil {
		t.Fatal(err)
	}
	if metas != nil {
		t.Errorf("expected nil for missing directory, got %v", metas)
	}
}

func TestDeleteMeta(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := WriteMeta(Meta{Name: "todelete", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := DeleteMeta("todelete"); err != nil {
		t.Fatal(err)
	}

	_, err := ReadMeta("todelete")
	if err == nil {
		t.Error("expected error after deletion")
	}
}

func TestDevcontainersDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dir := DevcontainersDir()
	expected := filepath.Join(tmp, ".human", "devcontainers")
	if dir != expected {
		t.Errorf("DevcontainersDir() = %q, want %q", dir, expected)
	}
}

func TestListMetas_SkipsCorruptFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Write a valid entry.
	if err := WriteMeta(Meta{
		Name:      "valid",
		Status:    StatusRunning,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// Write corrupt JSON directly.
	corruptPath := filepath.Join(DevcontainersDir(), "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	metas, err := ListMetas()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Errorf("expected 1 valid meta, got %d", len(metas))
	}
	if len(metas) > 0 && metas[0].Name != "valid" {
		t.Errorf("expected 'valid', got %q", metas[0].Name)
	}
}

func TestListMetas_SkipsNonJSONAndDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dcDir := DevcontainersDir()
	if err := os.MkdirAll(dcDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Create a non-JSON file.
	if err := os.WriteFile(filepath.Join(dcDir, "readme.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory.
	if err := os.MkdirAll(filepath.Join(dcDir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}

	metas, err := ListMetas()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 0 {
		t.Errorf("expected 0 metas, got %d", len(metas))
	}
}

func TestDeleteMeta_NonExistent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Mirrors internal/agent's DeleteMeta: an already-absent meta file must
	// not be reported as a failure (SC-1600).
	err := DeleteMeta("nonexistent")
	if err != nil {
		t.Errorf("expected nil deleting already-absent meta, got %v", err)
	}
}

func TestWriteMeta_OverwritesExisting(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	meta := Meta{
		Name:      "overwrite-dc",
		Status:    StatusRunning,
		CreatedAt: time.Now().Truncate(time.Second),
	}
	if err := WriteMeta(meta); err != nil {
		t.Fatal(err)
	}

	meta.Status = StatusStopped
	meta.StoppedAt = time.Now().Truncate(time.Second)
	if err := WriteMeta(meta); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMeta("overwrite-dc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusStopped {
		t.Errorf("status = %q, want %q", got.Status, StatusStopped)
	}
}

func TestReadMeta_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	dcDir := DevcontainersDir()
	if err := os.MkdirAll(dcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := MetaPath("bad-json")
	if err := os.WriteFile(path, []byte("{invalid json}"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ReadMeta("bad-json")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestMetaPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got := MetaPath("my-dc")
	want := filepath.Join(tmp, ".human", "devcontainers", "my-dc.json")
	if got != want {
		t.Errorf("MetaPath = %q, want %q", got, want)
	}
}
