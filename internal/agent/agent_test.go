package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadMeta(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	meta := Meta{
		Name:          "test-agent",
		ContainerID:   "abc123",
		ContainerName: ContainerName("test-agent"),
		Cwd:           "/home/user/project",
		Prompt:        "implement feature X",
		Status:        StatusRunning,
		CreatedAt:     time.Now().Truncate(time.Second),
		SkipPerms:     true,
		Model:         "opus",
	}

	if err := WriteMeta(meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	got, err := ReadMeta("test-agent")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}

	if got.Name != meta.Name {
		t.Errorf("Name = %q, want %q", got.Name, meta.Name)
	}
	if got.ContainerName != meta.ContainerName {
		t.Errorf("ContainerName = %q, want %q", got.ContainerName, meta.ContainerName)
	}
	if got.Cwd != meta.Cwd {
		t.Errorf("Cwd = %q, want %q", got.Cwd, meta.Cwd)
	}
	if got.Prompt != meta.Prompt {
		t.Errorf("Prompt = %q, want %q", got.Prompt, meta.Prompt)
	}
	if got.Status != meta.Status {
		t.Errorf("Status = %q, want %q", got.Status, meta.Status)
	}
	if !got.CreatedAt.Equal(meta.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, meta.CreatedAt)
	}
	if got.SkipPerms != meta.SkipPerms {
		t.Errorf("SkipPerms = %v, want %v", got.SkipPerms, meta.SkipPerms)
	}
	if got.Model != meta.Model {
		t.Errorf("Model = %q, want %q", got.Model, meta.Model)
	}
}

func TestListMetas_empty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	metas, err := ListMetas()
	if err != nil {
		t.Fatalf("ListMetas: %v", err)
	}
	if metas != nil {
		t.Errorf("expected nil metas for empty dir, got %d", len(metas))
	}
}

func TestListMetas_multiple(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for _, name := range []string{"agent-a", "agent-b"} {
		if err := WriteMeta(Meta{
			Name:          name,
			ContainerName: ContainerName(name),
			Status:        StatusRunning,
			CreatedAt:     time.Now(),
		}); err != nil {
			t.Fatalf("WriteMeta(%s): %v", name, err)
		}
	}

	metas, err := ListMetas()
	if err != nil {
		t.Fatalf("ListMetas: %v", err)
	}
	if len(metas) != 2 {
		t.Errorf("expected 2 metas, got %d", len(metas))
	}
}

func TestDeleteMeta(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := WriteMeta(Meta{
		Name:          "delete-me",
		ContainerName: ContainerName("delete-me"),
		Status:        StatusStopped,
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	if err := DeleteMeta("delete-me"); err != nil {
		t.Fatalf("DeleteMeta: %v", err)
	}

	if _, err := ReadMeta("delete-me"); err == nil {
		t.Fatal("expected error reading deleted meta")
	}
}

func TestContainerName(t *testing.T) {
	got := ContainerName("my-agent")
	want := "human-agent-my-agent"
	if got != want {
		t.Errorf("ContainerName = %q, want %q", got, want)
	}
}

func TestMetaPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got := MetaPath("test")
	want := filepath.Join(os.Getenv("HOME"), ".human", "agents", "test.json")
	if got != want {
		t.Errorf("MetaPath = %q, want %q", got, want)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
		{25 * time.Hour, "1d1h"},
		{48 * time.Hour, "2d"},
		{49 * time.Hour, "2d1h"},
	}

	for _, tt := range tests {
		got := FormatDuration(tt.d)
		if got != tt.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestListMetas_SkipsCorruptFiles(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Write a valid agent.
	if err := WriteMeta(Meta{
		Name:          "valid-agent",
		ContainerName: ContainerName("valid-agent"),
		Status:        StatusRunning,
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	// Write a corrupt JSON file directly.
	corruptPath := filepath.Join(AgentsDir(), "corrupt-agent.json")
	if err := os.WriteFile(corruptPath, []byte("not valid json{{{"), 0o600); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}

	metas, err := ListMetas()
	if err != nil {
		t.Fatalf("ListMetas: %v", err)
	}
	if len(metas) != 1 {
		t.Errorf("expected 1 valid meta (skipping corrupt), got %d", len(metas))
	}
	if len(metas) > 0 && metas[0].Name != "valid-agent" {
		t.Errorf("expected valid-agent, got %q", metas[0].Name)
	}
}

func TestListMetas_SkipsDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Create the agents directory.
	agentsDir := AgentsDir()
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Create a subdirectory that should be skipped.
	if err := os.MkdirAll(filepath.Join(agentsDir, "subdir"), 0o700); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}

	// Create a non-JSON file that should be skipped.
	if err := os.WriteFile(filepath.Join(agentsDir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing txt file: %v", err)
	}

	metas, err := ListMetas()
	if err != nil {
		t.Fatalf("ListMetas: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("expected 0 metas, got %d", len(metas))
	}
}

func TestDeleteMeta_NonExistent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Two independent cleanup paths (the zombie sweep and the session-end
	// auto-clean) are serialized by a per-name lock but can both still call
	// DeleteMeta for the same agent; the loser must not treat "already gone"
	// as failure (SC-1600).
	err := DeleteMeta("does-not-exist")
	if err != nil {
		t.Fatalf("expected nil deleting already-absent meta, got %v", err)
	}
}

func TestReadMeta_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Create agents dir and write corrupt JSON.
	agentsDir := AgentsDir()
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := MetaPath("bad-json")
	if err := os.WriteFile(path, []byte("{invalid json}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := ReadMeta("bad-json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestContainerName_Various(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"agent1", "human-agent-agent1"},
		{"my-agent", "human-agent-my-agent"},
		{"a", "human-agent-a"},
		{"", "human-agent-"},
	}
	for _, tt := range tests {
		got := ContainerName(tt.input)
		if got != tt.want {
			t.Errorf("ContainerName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestWriteMeta_OverwritesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	meta := Meta{
		Name:          "overwrite-me",
		ContainerName: ContainerName("overwrite-me"),
		Status:        StatusRunning,
		CreatedAt:     time.Now().Truncate(time.Second),
	}
	if err := WriteMeta(meta); err != nil {
		t.Fatalf("first WriteMeta: %v", err)
	}

	// Update status and overwrite.
	meta.Status = StatusStopped
	meta.StoppedAt = time.Now().Truncate(time.Second)
	if err := WriteMeta(meta); err != nil {
		t.Fatalf("second WriteMeta: %v", err)
	}

	got, err := ReadMeta("overwrite-me")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if got.Status != StatusStopped {
		t.Errorf("Status = %q, want %q", got.Status, StatusStopped)
	}
}
