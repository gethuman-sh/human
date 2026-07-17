package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/devcontainer"
)

// mockDockerClient implements devcontainer.DockerClient for agent tests.
type mockDockerClient struct {
	mu sync.Mutex

	inspectRunning bool
	inspectErr     error

	stopCalls   []string
	removeCalls []string
}

func (m *mockDockerClient) ImageBuild(_ context.Context, _ io.Reader, _ devcontainer.ImageBuildOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (m *mockDockerClient) ImagePull(_ context.Context, _ string, _ devcontainer.ImagePullOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (m *mockDockerClient) ImageInspect(_ context.Context, _ string) (devcontainer.ImageInspectResponse, error) {
	return devcontainer.ImageInspectResponse{}, nil
}
func (m *mockDockerClient) ImageList(_ context.Context, _ devcontainer.ImageListOptions) ([]devcontainer.ImageSummary, error) {
	return nil, nil
}
func (m *mockDockerClient) ContainerCreate(_ context.Context, _ devcontainer.ContainerCreateOptions) (string, error) {
	return "mock-id", nil
}
func (m *mockDockerClient) ContainerStart(_ context.Context, _ string) error { return nil }
func (m *mockDockerClient) ContainerStop(_ context.Context, id string, _ *int) error {
	m.mu.Lock()
	m.stopCalls = append(m.stopCalls, id)
	m.mu.Unlock()
	return nil
}
func (m *mockDockerClient) ContainerRemove(_ context.Context, id string, _ devcontainer.ContainerRemoveOptions) error {
	m.mu.Lock()
	m.removeCalls = append(m.removeCalls, id)
	m.mu.Unlock()
	return nil
}
func (m *mockDockerClient) ContainerInspect(_ context.Context, _ string) (devcontainer.ContainerInspectResponse, error) {
	if m.inspectErr != nil {
		return devcontainer.ContainerInspectResponse{}, m.inspectErr
	}
	return devcontainer.ContainerInspectResponse{
		State: devcontainer.ContainerState{Running: m.inspectRunning},
	}, nil
}
func (m *mockDockerClient) ContainerList(_ context.Context, _ devcontainer.ContainerListOptions) ([]devcontainer.ContainerSummary, error) {
	return nil, nil
}
func (m *mockDockerClient) ContainerLogs(_ context.Context, _ string, _ devcontainer.LogsOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (m *mockDockerClient) ContainerCommit(_ context.Context, _ string, _ string, _ map[string]string) (string, error) {
	return "sha256:committed", nil
}
func (m *mockDockerClient) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}
func (m *mockDockerClient) CopyFromContainer(_ context.Context, _, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (m *mockDockerClient) ExecCreate(_ context.Context, _ string, _ []string, _ devcontainer.ExecOptions) (string, error) {
	return "exec-1", nil
}
func (m *mockDockerClient) ExecAttach(_ context.Context, _ string) (devcontainer.ExecAttachResponse, error) {
	return devcontainer.ExecAttachResponse{
		Reader: strings.NewReader(""),
		Conn:   io.NopCloser(strings.NewReader("")),
	}, nil
}
func (m *mockDockerClient) ExecInspect(_ context.Context, _ string) (devcontainer.ExecInspectResponse, error) {
	return devcontainer.ExecInspectResponse{}, nil
}
func (m *mockDockerClient) Close() error { return nil }

func TestIsValidName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"agent1", true},
		{"my-agent", true},
		{"my_agent", true},
		{"Agent-1", true},
		{"-invalid", false},
		{"_invalid", false},
		{"", false},
		{"has space", false},
		{"has.dot", false},
	}

	for _, tt := range tests {
		if got := isValidName(tt.name); got != tt.valid {
			t.Errorf("isValidName(%q) = %v, want %v", tt.name, got, tt.valid)
		}
	}
}

func TestBuildClaudeArgs(t *testing.T) {
	mgr := &Manager{}

	args := mgr.BuildClaudeArgs(StartOpts{})
	if len(args) != 1 || args[0] != "--permission-mode=auto" {
		t.Errorf("default args = %v", args)
	}

	args = mgr.BuildClaudeArgs(StartOpts{SkipPerms: true, Model: "opus"})
	found := map[string]bool{}
	for _, a := range args {
		found[a] = true
	}
	if !found["--dangerously-skip-permissions"] {
		t.Error("missing --dangerously-skip-permissions")
	}
	if !found["opus"] {
		t.Error("missing model opus")
	}
}

func TestResolveDirectories_DefaultWorkspace(t *testing.T) {
	opts := StartOpts{}
	workspace, configDir := resolveDirectories(opts)
	if workspace != "." {
		t.Errorf("workspace = %q, want %q", workspace, ".")
	}
	// When no ConfigDir and no .humanconfig, configDir falls back to workspace.
	if configDir != "." {
		t.Errorf("configDir = %q, want %q", configDir, ".")
	}
}

func TestResolveDirectories_ExplicitWorkspace(t *testing.T) {
	opts := StartOpts{
		Workspace: "/my/project",
		ConfigDir: "/my/config",
	}
	workspace, configDir := resolveDirectories(opts)
	if workspace != "/my/project" {
		t.Errorf("workspace = %q, want %q", workspace, "/my/project")
	}
	if configDir != "/my/config" {
		t.Errorf("configDir = %q, want %q", configDir, "/my/config")
	}
}

func TestResolveDirectories_WorkspaceWithoutConfig(t *testing.T) {
	// When workspace is set but configDir is not and no .humanconfig exists,
	// configDir should fall back to workspace.
	tmpDir := t.TempDir()
	opts := StartOpts{
		Workspace: tmpDir,
	}
	workspace, configDir := resolveDirectories(opts)
	if workspace != tmpDir {
		t.Errorf("workspace = %q, want %q", workspace, tmpDir)
	}
	if configDir != tmpDir {
		t.Errorf("configDir = %q, want %q", configDir, tmpDir)
	}
}

func TestIsValidName_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"a", true},
		{"A", true},
		{"1agent", true},
		{"agent-with-many-hyphens", true},
		{"agent_with_underscores", true},
		{"agent123", true},
		{"UPPER-CASE", true},
		{"-starts-with-hyphen", false},
		{"_starts-with-underscore", false},
		{"has/slash", false},
		{"has@at", false},
		{"has:colon", false},
		{"a b", false},
	}
	for _, tt := range tests {
		got := isValidName(tt.name)
		if got != tt.valid {
			t.Errorf("isValidName(%q) = %v, want %v", tt.name, got, tt.valid)
		}
	}
}

func TestBuildClaudeArgs_ModelOnly(t *testing.T) {
	mgr := &Manager{}
	args := mgr.BuildClaudeArgs(StartOpts{Model: "sonnet"})

	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[0] != "--permission-mode=auto" {
		t.Errorf("args[0] = %q, want %q", args[0], "--permission-mode=auto")
	}
	if args[1] != "--model" {
		t.Errorf("args[1] = %q, want %q", args[1], "--model")
	}
	if args[2] != "sonnet" {
		t.Errorf("args[2] = %q, want %q", args[2], "sonnet")
	}
}

func TestBuildClaudeArgs_SkipPermsOnly(t *testing.T) {
	mgr := &Manager{}
	args := mgr.BuildClaudeArgs(StartOpts{SkipPerms: true})

	if len(args) != 1 {
		t.Fatalf("expected 1 arg, got %d: %v", len(args), args)
	}
	if args[0] != "--dangerously-skip-permissions" {
		t.Errorf("args[0] = %q, want %q", args[0], "--dangerously-skip-permissions")
	}
}

func TestIsContainerAlive_Running(t *testing.T) {
	mock := &mockDockerClient{inspectRunning: true}
	mgr := &Manager{Docker: mock}
	if !mgr.isContainerAlive(context.Background(), "container-123") {
		t.Error("expected isContainerAlive to return true for running container")
	}
}

func TestIsContainerAlive_Stopped(t *testing.T) {
	mock := &mockDockerClient{inspectRunning: false}
	mgr := &Manager{Docker: mock}
	if mgr.isContainerAlive(context.Background(), "container-123") {
		t.Error("expected isContainerAlive to return false for stopped container")
	}
}

func TestIsContainerAlive_EmptyID(t *testing.T) {
	mock := &mockDockerClient{}
	mgr := &Manager{Docker: mock}
	if mgr.isContainerAlive(context.Background(), "") {
		t.Error("expected isContainerAlive to return false for empty ID")
	}
}

func TestIsContainerAlive_InspectError(t *testing.T) {
	mock := &mockDockerClient{inspectErr: fmt.Errorf("container not found")}
	mgr := &Manager{Docker: mock}
	if mgr.isContainerAlive(context.Background(), "nonexistent") {
		t.Error("expected isContainerAlive to return false when inspect fails")
	}
}

func TestAttach_Success(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := WriteMeta(Meta{
		Name:          "attach-agent",
		ContainerID:   "abc123",
		ContainerName: ContainerName("attach-agent"),
		Status:        StatusRunning,
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	mgr := &Manager{Docker: &mockDockerClient{}}
	meta, err := mgr.Attach(context.Background(), "attach-agent")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ContainerName != ContainerName("attach-agent") {
		t.Errorf("ContainerName = %q, want %q", meta.ContainerName, ContainerName("attach-agent"))
	}
}

func TestAttach_NoContainer(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := WriteMeta(Meta{
		Name:          "no-container",
		ContainerName: "", // empty container name
		Status:        StatusRunning,
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	mgr := &Manager{Docker: &mockDockerClient{}}
	_, err := mgr.Attach(context.Background(), "no-container")
	if err == nil {
		t.Error("expected error when agent has no container")
	}
}

func TestAttach_NotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mgr := &Manager{Docker: &mockDockerClient{}}
	_, err := mgr.Attach(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent agent")
	}
}

func TestStop(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := WriteMeta(Meta{
		Name:          "stop-agent",
		ContainerID:   "stop-container-id",
		ContainerName: ContainerName("stop-agent"),
		Status:        StatusRunning,
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	mock := &mockDockerClient{}
	mgr := &Manager{Docker: mock}
	if err := mgr.Stop(context.Background(), "stop-agent"); err != nil {
		t.Fatal(err)
	}

	// Verify stop and remove were called.
	if len(mock.stopCalls) != 1 || mock.stopCalls[0] != "stop-container-id" {
		t.Errorf("stopCalls = %v, want [stop-container-id]", mock.stopCalls)
	}
	if len(mock.removeCalls) != 1 || mock.removeCalls[0] != "stop-container-id" {
		t.Errorf("removeCalls = %v, want [stop-container-id]", mock.removeCalls)
	}

	// Verify metadata updated to stopped.
	meta, err := ReadMeta("stop-agent")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusStopped {
		t.Errorf("status = %q, want %q", meta.Status, StatusStopped)
	}
	if meta.StoppedAt.IsZero() {
		t.Error("StoppedAt should be set")
	}
}

func TestStop_NotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mgr := &Manager{Docker: &mockDockerClient{}}
	err := mgr.Stop(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error stopping nonexistent agent")
	}
}

func TestStop_EmptyContainerID(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Agent with no container ID -- Stop should just update status.
	if err := WriteMeta(Meta{
		Name:      "no-cid",
		Status:    StatusRunning,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{}
	mgr := &Manager{Docker: mock}
	if err := mgr.Stop(context.Background(), "no-cid"); err != nil {
		t.Fatal(err)
	}

	// Should not have called stop/remove on Docker.
	if len(mock.stopCalls) != 0 {
		t.Errorf("stopCalls = %v, want empty", mock.stopCalls)
	}

	meta, err := ReadMeta("no-cid")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusStopped {
		t.Errorf("status = %q, want %q", meta.Status, StatusStopped)
	}
}

func TestDelete(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := WriteMeta(Meta{
		Name:          "delete-agent",
		ContainerID:   "delete-id",
		ContainerName: ContainerName("delete-agent"),
		Status:        StatusRunning,
		CreatedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	mgr := &Manager{Docker: &mockDockerClient{}}
	if err := mgr.Delete(context.Background(), "delete-agent"); err != nil {
		t.Fatal(err)
	}

	// Metadata should be gone.
	_, err := ReadMeta("delete-agent")
	if err == nil {
		t.Error("expected error reading deleted agent metadata")
	}
}

func TestRefresh_UpdatesStoppedContainers(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Create two agents: one running, one running.
	for _, name := range []string{"alive", "dead"} {
		if err := WriteMeta(Meta{
			Name:        name,
			ContainerID: name + "-id",
			Status:      StatusRunning,
			CreatedAt:   time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Mock: only "alive-id" is running.
	mock := &aliveOrDeadMock{alive: map[string]bool{"alive-id": true}}
	mgr := &Manager{Docker: mock}
	if err := mgr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	// "alive" should still be running.
	aliveMeta, err := ReadMeta("alive")
	if err != nil {
		t.Fatal(err)
	}
	if aliveMeta.Status != StatusRunning {
		t.Errorf("alive status = %q, want %q", aliveMeta.Status, StatusRunning)
	}

	// "dead" should be updated to stopped.
	deadMeta, err := ReadMeta("dead")
	if err != nil {
		t.Fatal(err)
	}
	if deadMeta.Status != StatusStopped {
		t.Errorf("dead status = %q, want %q", deadMeta.Status, StatusStopped)
	}
}

func TestRefresh_SkipsAlreadyStopped(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := WriteMeta(Meta{
		Name:      "stopped",
		Status:    StatusStopped,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{}
	mgr := &Manager{Docker: mock}
	if err := mgr.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Status should remain stopped; no Docker calls needed.
}

// aliveOrDeadMock implements devcontainer.DockerClient and returns Running=true
// only for container IDs present in the alive map.
type aliveOrDeadMock struct {
	mockDockerClient
	alive map[string]bool
}

func (m *aliveOrDeadMock) ContainerInspect(_ context.Context, id string) (devcontainer.ContainerInspectResponse, error) {
	if m.alive[id] {
		return devcontainer.ContainerInspectResponse{
			State: devcontainer.ContainerState{Running: true},
		}, nil
	}
	return devcontainer.ContainerInspectResponse{
		State: devcontainer.ContainerState{Running: false},
	}, nil
}

func TestStartInvalidName(t *testing.T) {
	mgr := &Manager{Docker: &mockDockerClient{}}
	_, err := mgr.Start(context.Background(), StartOpts{Name: "-invalid"})
	if err == nil {
		t.Error("expected error for invalid agent name")
	}
}
