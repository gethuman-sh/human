package devcontainer

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
)

// mockDockerClient implements DockerClient for testing.
type mockDockerClient struct {
	mu         sync.Mutex
	execCalls  []mockExecCall
	execOutput string
	execExit   int
	execErr    error

	createCalls []ContainerCreateOptions
	createID    string

	startCalls  []string
	stopCalls   []string
	removeCalls []string

	inspectState  ContainerState
	inspectLabels map[string]string

	listResult []ContainerSummary

	pullCalls []string

	imageInspectErr    error
	imageInspectResult ImageInspectResponse

	commitCalls []string
	commitID    string
	commitEnv   map[string]string
}

type mockExecCall struct {
	ContainerID string
	Cmd         []string
	Opts        ExecOptions
}

func (m *mockDockerClient) ImageBuild(_ context.Context, _ io.Reader, _ ImageBuildOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (m *mockDockerClient) ImagePull(_ context.Context, ref string, _ ImagePullOptions) (io.ReadCloser, error) {
	m.mu.Lock()
	m.pullCalls = append(m.pullCalls, ref)
	m.mu.Unlock()
	return io.NopCloser(strings.NewReader("")), nil
}

func (m *mockDockerClient) ImageInspect(_ context.Context, _ string) (ImageInspectResponse, error) {
	if m.imageInspectErr != nil {
		return ImageInspectResponse{}, m.imageInspectErr
	}
	return m.imageInspectResult, nil
}

func (m *mockDockerClient) ImageList(_ context.Context, _ ImageListOptions) ([]ImageSummary, error) {
	return nil, nil
}

func (m *mockDockerClient) ContainerCreate(_ context.Context, opts ContainerCreateOptions) (string, error) {
	m.mu.Lock()
	m.createCalls = append(m.createCalls, opts)
	m.mu.Unlock()
	id := m.createID
	if id == "" {
		id = "mock-container-id"
	}
	return id, nil
}

func (m *mockDockerClient) ContainerStart(_ context.Context, id string) error {
	m.mu.Lock()
	m.startCalls = append(m.startCalls, id)
	m.mu.Unlock()
	return nil
}

func (m *mockDockerClient) ContainerStop(_ context.Context, id string, _ *int) error {
	m.mu.Lock()
	m.stopCalls = append(m.stopCalls, id)
	m.mu.Unlock()
	return nil
}

func (m *mockDockerClient) ContainerRemove(_ context.Context, id string, _ ContainerRemoveOptions) error {
	m.mu.Lock()
	m.removeCalls = append(m.removeCalls, id)
	m.mu.Unlock()
	return nil
}

func (m *mockDockerClient) ContainerInspect(_ context.Context, _ string) (ContainerInspectResponse, error) {
	return ContainerInspectResponse{
		ID:    "mock-container-id",
		State: m.inspectState,
		Config: ContainerConfigInfo{
			Labels: m.inspectLabels,
		},
	}, nil
}

func (m *mockDockerClient) ContainerList(_ context.Context, _ ContainerListOptions) ([]ContainerSummary, error) {
	return m.listResult, nil
}

func (m *mockDockerClient) ContainerLogs(_ context.Context, _ string, _ LogsOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("log output")), nil
}

func (m *mockDockerClient) ContainerCommit(_ context.Context, id string, ref string, env map[string]string) (string, error) {
	m.mu.Lock()
	m.commitCalls = append(m.commitCalls, id+":"+ref)
	m.commitEnv = env
	m.mu.Unlock()
	if m.commitID != "" {
		return m.commitID, nil
	}
	return "sha256:committed", nil
}

func (m *mockDockerClient) ExecCreate(_ context.Context, containerID string, cmd []string, opts ExecOptions) (string, error) {
	m.mu.Lock()
	m.execCalls = append(m.execCalls, mockExecCall{
		ContainerID: containerID,
		Cmd:         cmd,
		Opts:        opts,
	})
	m.mu.Unlock()
	if m.execErr != nil {
		return "", m.execErr
	}
	return fmt.Sprintf("exec-%d", len(m.execCalls)), nil
}

func (m *mockDockerClient) ExecAttach(_ context.Context, _ string) (ExecAttachResponse, error) {
	output := m.execOutput
	return ExecAttachResponse{
		Reader: strings.NewReader(output),
		Conn:   io.NopCloser(strings.NewReader("")),
	}, nil
}

func (m *mockDockerClient) ExecInspect(_ context.Context, _ string) (ExecInspectResponse, error) {
	return ExecInspectResponse{ExitCode: m.execExit}, nil
}

func (m *mockDockerClient) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

func (m *mockDockerClient) Close() error { return nil }

func testLogger() zerolog.Logger {
	return zerolog.New(io.Discard)
}

func TestRunHook_Nil(t *testing.T) {
	err := RunHook(context.Background(), &mockDockerClient{}, "cid", "user", nil, testLogger())
	if err != nil {
		t.Errorf("expected nil error for nil hook, got %v", err)
	}
}

func TestRunHook_String(t *testing.T) {
	mock := &mockDockerClient{}
	err := RunHook(context.Background(), mock, "cid", "vscode", "echo hello", testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.execCalls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(mock.execCalls))
	}
	call := mock.execCalls[0]
	if call.ContainerID != "cid" {
		t.Errorf("containerID = %q", call.ContainerID)
	}
	if len(call.Cmd) != 3 || call.Cmd[0] != "/bin/sh" || call.Cmd[2] != "echo hello" {
		t.Errorf("cmd = %v", call.Cmd)
	}
	if call.Opts.User != "vscode" {
		t.Errorf("user = %q", call.Opts.User)
	}
}

func TestRunHook_EmptyString(t *testing.T) {
	mock := &mockDockerClient{}
	err := RunHook(context.Background(), mock, "cid", "user", "", testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.execCalls) != 0 {
		t.Errorf("expected 0 exec calls for empty string, got %d", len(mock.execCalls))
	}
}

func TestRunHook_Array(t *testing.T) {
	mock := &mockDockerClient{}
	cmd := []interface{}{"npm", "install"}
	err := RunHook(context.Background(), mock, "cid", "user", cmd, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.execCalls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(mock.execCalls))
	}
	if mock.execCalls[0].Cmd[0] != "npm" || mock.execCalls[0].Cmd[1] != "install" {
		t.Errorf("cmd = %v", mock.execCalls[0].Cmd)
	}
}

func TestRunHook_Map(t *testing.T) {
	mock := &mockDockerClient{}
	cmd := map[string]interface{}{
		"setup": "make setup",
		"lint":  "make lint",
	}
	err := RunHook(context.Background(), mock, "cid", "user", cmd, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	// Two parallel commands -> 2 exec calls.
	if len(mock.execCalls) != 2 {
		t.Errorf("expected 2 exec calls, got %d", len(mock.execCalls))
	}
}

func TestRunHook_ExecFailure(t *testing.T) {
	mock := &mockDockerClient{execExit: 1}
	err := RunHook(context.Background(), mock, "cid", "user", "exit 1", testLogger())
	if err == nil {
		t.Error("expected error for non-zero exit")
	}
}

func TestRunHook_UnsupportedType(t *testing.T) {
	err := RunHook(context.Background(), &mockDockerClient{}, "cid", "user", 42, testLogger())
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestRunHook_EmptyArray(t *testing.T) {
	mock := &mockDockerClient{}
	cmd := []interface{}{}
	err := RunHook(context.Background(), mock, "cid", "user", cmd, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.execCalls) != 0 {
		t.Errorf("expected 0 exec calls for empty array, got %d", len(mock.execCalls))
	}
}

func TestRunHook_ArrayWithNonString(t *testing.T) {
	mock := &mockDockerClient{}
	cmd := []interface{}{"echo", 42}
	err := RunHook(context.Background(), mock, "cid", "user", cmd, testLogger())
	if err == nil {
		t.Error("expected error for non-string element in array")
	}
}

func TestExecAttachResponse_Close_NilConn(t *testing.T) {
	resp := ExecAttachResponse{
		Reader: strings.NewReader("output"),
		Conn:   nil,
	}
	err := resp.Close()
	if err != nil {
		t.Errorf("Close with nil Conn should return nil, got %v", err)
	}
}

func TestExecAttachResponse_Close_WithConn(t *testing.T) {
	resp := ExecAttachResponse{
		Reader: strings.NewReader("output"),
		Conn:   io.NopCloser(strings.NewReader("")),
	}
	err := resp.Close()
	if err != nil {
		t.Errorf("Close should return nil, got %v", err)
	}
}

func TestRunLifecycleHooks_AllNil(t *testing.T) {
	mock := &mockDockerClient{}
	cfg := &DevcontainerConfig{} // all hooks nil
	var buf strings.Builder
	err := RunLifecycleHooks(context.Background(), mock, "cid", "user", cfg, testLogger(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.execCalls) != 0 {
		t.Errorf("expected 0 exec calls when all hooks are nil, got %d", len(mock.execCalls))
	}
}

func TestRunLifecycleHooks(t *testing.T) {
	mock := &mockDockerClient{}
	cfg := &DevcontainerConfig{
		OnCreateCommand:  "echo oncreate",
		PostStartCommand: "echo poststart",
	}
	var buf strings.Builder
	err := RunLifecycleHooks(context.Background(), mock, "cid", "user", cfg, testLogger(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	// Two hooks defined -> 2 exec calls.
	if len(mock.execCalls) != 2 {
		t.Errorf("expected 2 exec calls, got %d", len(mock.execCalls))
	}
	output := buf.String()
	if !strings.Contains(output, "onCreateCommand") {
		t.Errorf("output should mention onCreateCommand: %s", output)
	}
	if !strings.Contains(output, "postStartCommand") {
		t.Errorf("output should mention postStartCommand: %s", output)
	}
}
