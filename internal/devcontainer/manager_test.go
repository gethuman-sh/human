package devcontainer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/StephanSchmidt/human/internal/daemon"
)

// setupTestProject creates a temp project dir with a devcontainer.json.
func setupTestProject(t *testing.T, configJSON string) (string, *mockDockerClient, *pullThenInspectMock) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	projectDir := filepath.Join(tmp, "myproject")
	dcDir := filepath.Join(projectDir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dcDir, "devcontainer.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{
		imageInspectErr:    fmt.Errorf("not found"),
		imageInspectResult: ImageInspectResponse{ID: "sha256:pulled"},
		createID:           "container-abc123",
		inspectState:       ContainerState{Running: true, Status: "running"},
	}
	callCount := 0
	docker := &pullThenInspectMock{
		mockDockerClient: mock,
		inspectCallCount: &callCount,
		inspectErr:       fmt.Errorf("not found"),
		inspectResult:    ImageInspectResponse{ID: "sha256:pulled", Tags: []string{"ubuntu:22.04"}},
	}
	return projectDir, mock, docker
}

func TestManager_Up_NewContainer(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"name": "test", "image": "ubuntu:22.04", "remoteUser": "vscode"}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	var buf bytes.Buffer
	meta, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if meta.Status != StatusRunning {
		t.Errorf("status = %q, want %q", meta.Status, StatusRunning)
	}
	if meta.ContainerID != "container-abc123" {
		t.Errorf("containerID = %q", meta.ContainerID)
	}
	if meta.RemoteUser != "vscode" {
		t.Errorf("remoteUser = %q", meta.RemoteUser)
	}

	verifyContainerCreate(t, mock, projectDir)
	verifyMetaPersisted(t, meta.Name)

	if !strings.Contains(buf.String(), "Devcontainer running") {
		t.Errorf("output should contain success message: %s", buf.String())
	}
}

func verifyContainerCreate(t *testing.T, mock *mockDockerClient, projectDir string) {
	t.Helper()
	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}
	create := mock.createCalls[0]
	if create.Name != ContainerName(projectDir) {
		t.Errorf("container name = %q", create.Name)
	}
	if create.Labels[LabelManaged] != "true" {
		t.Error("missing managed label")
	}
	if create.Labels[LabelProject] != projectDir {
		t.Errorf("project label = %q", create.Labels[LabelProject])
	}
	if len(mock.startCalls) != 1 {
		t.Errorf("expected 1 start call, got %d", len(mock.startCalls))
	}
}

func verifyMetaPersisted(t *testing.T, name string) {
	t.Helper()
	persisted, err := ReadMeta(name)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ContainerID != "container-abc123" {
		t.Errorf("persisted containerID = %q", persisted.ContainerID)
	}
}

func TestManager_Up_DaemonInjection(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu"}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	daemonInfo := &daemon.DaemonInfo{
		Addr:  "192.168.1.5:19285",
		Token: "secret-token",
	}
	_, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		DaemonInfo: daemonInfo,
		Out:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}
	env := mock.createCalls[0].Env
	found := map[string]bool{}
	for _, e := range env {
		if strings.HasPrefix(e, "HUMAN_DAEMON_TOKEN=") {
			found["token"] = true
			if !strings.Contains(e, "secret-token") {
				t.Errorf("daemon token not injected: %s", e)
			}
		}
		if strings.HasPrefix(e, "HUMAN_DAEMON_ADDR=") {
			found["addr"] = true
		}
		if strings.HasPrefix(e, "BROWSER=") {
			found["browser"] = true
		}
	}
	if !found["token"] || !found["addr"] || !found["browser"] {
		t.Errorf("missing daemon env vars: %v", found)
	}
}

func TestManager_Stop(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := WriteMeta(Meta{
		Name:        "mydc",
		ContainerID: "abc123",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{}
	mgr := &Manager{Docker: mock, Logger: testLogger()}
	if err := mgr.Stop(context.Background(), "mydc"); err != nil {
		t.Fatal(err)
	}

	if len(mock.stopCalls) != 1 || mock.stopCalls[0] != "abc123" {
		t.Errorf("stop calls = %v", mock.stopCalls)
	}

	meta, err := ReadMeta("mydc")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusStopped {
		t.Errorf("status = %q, want stopped", meta.Status)
	}
}

func TestManager_Down(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := WriteMeta(Meta{
		Name:        "mydc",
		ContainerID: "abc123",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{}
	mgr := &Manager{Docker: mock, Logger: testLogger()}
	if err := mgr.Down(context.Background(), "mydc", false); err != nil {
		t.Fatal(err)
	}

	if len(mock.removeCalls) != 1 || mock.removeCalls[0] != "abc123" {
		t.Errorf("remove calls = %v", mock.removeCalls)
	}

	_, err := ReadMeta("mydc")
	if err == nil {
		t.Error("metadata should be deleted after down")
	}
}

func TestManager_List(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	for _, name := range []string{"dc-a", "dc-b"} {
		if err := WriteMeta(Meta{
			Name:        name,
			ContainerID: name + "-id",
			Status:      StatusRunning,
			CreatedAt:   time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	mock := &mockDockerClient{
		inspectState: ContainerState{Running: true, Status: "running"},
	}
	mgr := &Manager{Docker: mock, Logger: testLogger()}
	metas, err := mgr.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Errorf("expected 2 metas, got %d", len(metas))
	}
}

func TestManager_Exec(t *testing.T) {
	mock := &mockDockerClient{}
	mgr := &Manager{Docker: mock, Logger: testLogger()}

	var stdout, stderr bytes.Buffer
	exitCode, err := mgr.Exec(context.Background(), "container-id", []string{"echo", "hello"}, "vscode", nil, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Errorf("exit code = %d", exitCode)
	}
	if len(mock.execCalls) != 1 {
		t.Errorf("expected 1 exec call, got %d", len(mock.execCalls))
	}
	call := mock.execCalls[0]
	if call.ContainerID != "container-id" {
		t.Errorf("container = %q", call.ContainerID)
	}
	if call.Opts.User != "vscode" {
		t.Errorf("user = %q", call.Opts.User)
	}
}

func TestParseRunArgs(t *testing.T) {
	opts := &ContainerCreateOptions{}
	args := []string{
		"--add-host=myhost:10.0.0.1",
		"--cap-add", "SYS_PTRACE",
		"--privileged",
		"--network=host",
		"--security-opt=seccomp=unconfined",
		"--unknown-flag",
	}
	ParseRunArgs(args, opts, testLogger())

	if len(opts.ExtraHosts) != 1 || opts.ExtraHosts[0] != "myhost:10.0.0.1" {
		t.Errorf("ExtraHosts = %v", opts.ExtraHosts)
	}
	if len(opts.CapAdd) != 1 || opts.CapAdd[0] != "SYS_PTRACE" {
		t.Errorf("CapAdd = %v", opts.CapAdd)
	}
	if !opts.Privileged {
		t.Error("expected Privileged = true")
	}
	if opts.NetworkMode != "host" {
		t.Errorf("NetworkMode = %q", opts.NetworkMode)
	}
	if len(opts.SecurityOpt) != 1 || opts.SecurityOpt[0] != "seccomp=unconfined" {
		t.Errorf("SecurityOpt = %v", opts.SecurityOpt)
	}
}

func TestParseMountString_BindMount(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Standard devcontainer.json mount format.
		{"source=/host/path,target=/container/path,type=bind", "/host/path:/container/path"},
		// With readonly.
		{"source=/host/path,target=/container/path,type=bind,readonly", "/host/path:/container/path:ro"},
		// Alternative key names.
		{"src=/a,dst=/b,type=bind", "/a:/b"},
		{"src=/a,destination=/b,type=bind", "/a:/b"},
		// Already in Binds format (passthrough).
		{"/host:/container", "/host:/container"},
		{"/host:/container:ro", "/host:/container:ro"},
		// Non-bind mount type (volume) should return empty.
		{"source=myvolume,target=/data,type=volume", ""},
		// Missing source or target.
		{"target=/container/path,type=bind", ""},
		{"source=/host/path,type=bind", ""},
		// No type specified (defaults to bind).
		{"source=/host/path,target=/container/path", "/host/path:/container/path"},
	}
	for _, tt := range tests {
		got := parseMountString(tt.input)
		if got != tt.want {
			t.Errorf("parseMountString(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseMountString_WithSpaces(t *testing.T) {
	input := "source=/host/path , target=/container/path , type=bind"
	got := parseMountString(input)
	if got != "/host/path:/container/path" {
		t.Errorf("parseMountString with spaces = %q, want %q", got, "/host/path:/container/path")
	}
}

func TestDeduplicateBinds(t *testing.T) {
	binds := []string{
		"/first:/target-a",
		"/second:/target-b",
		"/third:/target-a", // duplicate target, should replace /first
	}
	got := deduplicateBinds(binds)
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped binds, got %d: %v", len(got), got)
	}
	// The last entry for /target-a should win.
	foundA := false
	for _, b := range got {
		if strings.Contains(b, "/target-a") {
			foundA = true
			if !strings.HasPrefix(b, "/third:") {
				t.Errorf("expected /third:/target-a to win, got %q", b)
			}
		}
	}
	if !foundA {
		t.Error("missing /target-a entry")
	}
}

func TestDeduplicateBinds_NoConflicts(t *testing.T) {
	binds := []string{
		"/a:/x",
		"/b:/y",
		"/c:/z",
	}
	got := deduplicateBinds(binds)
	if len(got) != 3 {
		t.Errorf("expected 3 binds, got %d", len(got))
	}
}

func TestDeduplicateBinds_WithOptions(t *testing.T) {
	binds := []string{
		"/first:/target:ro",
		"/second:/target:rw", // same target, should replace
	}
	got := deduplicateBinds(binds)
	if len(got) != 1 {
		t.Fatalf("expected 1 bind, got %d: %v", len(got), got)
	}
	if got[0] != "/second:/target:rw" {
		t.Errorf("expected /second:/target:rw, got %q", got[0])
	}
}

func TestRemoteHome(t *testing.T) {
	tests := []struct {
		user string
		want string
	}{
		{"root", "/root"},
		{"", "/root"},
		{"vscode", "/home/vscode"},
		{"developer", "/home/developer"},
	}
	for _, tt := range tests {
		cfg := &DevcontainerConfig{RemoteUser: tt.user}
		got := remoteHome(cfg)
		if got != tt.want {
			t.Errorf("remoteHome(user=%q) = %q, want %q", tt.user, got, tt.want)
		}
	}
}

func TestManager_Status(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := WriteMeta(Meta{
		Name:        "status-dc",
		ContainerID: "status-id-123",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{
		inspectState: ContainerState{Running: true, Status: "running"},
	}
	mgr := &Manager{Docker: mock, Logger: testLogger()}
	meta, err := mgr.Status(context.Background(), "status-dc")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusRunning {
		t.Errorf("status = %q, want %q", meta.Status, StatusRunning)
	}
}

func TestManager_Status_Stopped(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := WriteMeta(Meta{
		Name:        "stopped-dc",
		ContainerID: "stopped-id-123",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{
		inspectState: ContainerState{Running: false, Status: "exited"},
	}
	mgr := &Manager{Docker: mock, Logger: testLogger()}
	meta, err := mgr.Status(context.Background(), "stopped-dc")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusStopped {
		t.Errorf("status = %q, want %q", meta.Status, StatusStopped)
	}
}

func TestManager_Status_ContainerGone(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := WriteMeta(Meta{
		Name:        "gone-dc",
		ContainerID: "gone-id-123",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// Return error on inspect to simulate container not found.
	mock := &mockDockerClient{}
	// Override ContainerInspect to return error by wrapping.
	inspectErrMock := &inspectErrorMock{mockDockerClient: mock}
	mgr := &Manager{Docker: inspectErrMock, Logger: testLogger()}
	meta, err := mgr.Status(context.Background(), "gone-dc")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusFailed {
		t.Errorf("status = %q, want %q", meta.Status, StatusFailed)
	}
}

// inspectErrorMock wraps mockDockerClient but returns an error on ContainerInspect.
type inspectErrorMock struct {
	*mockDockerClient
}

func (m *inspectErrorMock) ContainerInspect(_ context.Context, _ string) (ContainerInspectResponse, error) {
	return ContainerInspectResponse{}, fmt.Errorf("container not found")
}

func TestManager_Status_NotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mock := &mockDockerClient{}
	mgr := &Manager{Docker: mock, Logger: testLogger()}
	_, err := mgr.Status(context.Background(), "nonexistent-dc")
	if err == nil {
		t.Error("expected error for nonexistent devcontainer")
	}
}

func TestReadConfig(t *testing.T) {
	dir := t.TempDir()
	dcDir := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{
  // This is a comment
  "name": "test",
  "image": "ubuntu:22.04",
  "remoteUser": "vscode"
}`
	if err := os.WriteFile(filepath.Join(dcDir, "devcontainer.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "test" {
		t.Errorf("name = %q, want %q", cfg.Name, "test")
	}
	if cfg.Image != "ubuntu:22.04" {
		t.Errorf("image = %q, want %q", cfg.Image, "ubuntu:22.04")
	}
	if cfg.RemoteUser != "vscode" {
		t.Errorf("remoteUser = %q, want %q", cfg.RemoteUser, "vscode")
	}
}

func TestReadConfig_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadConfig(dir)
	if err == nil {
		t.Error("expected error when no devcontainer.json exists")
	}
}

func TestManager_Up_CustomContainerName(t *testing.T) {
	projectDir, _, docker := setupTestProject(t, `{"image": "ubuntu:22.04"}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	var buf bytes.Buffer
	meta, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir:    projectDir,
		ContainerName: "my-custom-name",
		Out:           &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta.ContainerName != "my-custom-name" {
		t.Errorf("container name = %q, want %q", meta.ContainerName, "my-custom-name")
	}
}

func TestManager_Up_WithMounts(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{
  "image": "ubuntu:22.04",
  "mounts": [
    "source=/host/data,target=/data,type=bind",
    "source=/host/config,target=/config,type=bind,readonly"
  ]
}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	_, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}
	binds := mock.createCalls[0].Binds
	foundData := false
	foundConfigRO := false
	for _, b := range binds {
		if b == "/host/data:/data" {
			foundData = true
		}
		if b == "/host/config:/config:ro" {
			foundConfigRO = true
		}
	}
	if !foundData {
		t.Errorf("missing /host/data:/data in binds: %v", binds)
	}
	if !foundConfigRO {
		t.Errorf("missing /host/config:/config:ro in binds: %v", binds)
	}
}

func TestManager_Up_WithCACert(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)

	// Create CA cert file in the test HOME.
	home := os.Getenv("HOME")
	humanDir := filepath.Join(home, ".human")
	if err := os.MkdirAll(humanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(humanDir, "ca.crt"), []byte("cert-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	_, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}
	binds := mock.createCalls[0].Binds
	foundCACert := false
	for _, b := range binds {
		if strings.Contains(b, "ca.crt") && strings.HasSuffix(b, ":ro") {
			foundCACert = true
			break
		}
	}
	if !foundCACert {
		t.Errorf("expected CA cert mount in binds: %v", binds)
	}
}

func TestManager_Up_WithClaudeDir(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)

	// Create .claude directory in the test HOME.
	home := os.Getenv("HOME")
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	_, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}
	binds := mock.createCalls[0].Binds
	foundClaude := false
	for _, b := range binds {
		if strings.Contains(b, ".claude") && !strings.Contains(b, ".claude.json") {
			foundClaude = true
			break
		}
	}
	if !foundClaude {
		t.Errorf("expected .claude dir mount in binds: %v", binds)
	}
}

func TestManager_Up_DefaultRemoteUser(t *testing.T) {
	projectDir, _, docker := setupTestProject(t, `{"image": "ubuntu:22.04"}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	meta, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// When no remoteUser is specified, it should default to "root".
	if meta.RemoteUser != "root" {
		t.Errorf("remoteUser = %q, want %q", meta.RemoteUser, "root")
	}
}

func TestManager_Up_DefaultWorkspaceFolder(t *testing.T) {
	projectDir, _, docker := setupTestProject(t, `{"image": "ubuntu:22.04"}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	meta, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Default workspace folder: /workspaces/<basename>.
	expected := "/workspaces/" + filepath.Base(projectDir)
	if meta.WorkspaceDir != expected {
		t.Errorf("workspaceDir = %q, want %q", meta.WorkspaceDir, expected)
	}
}

func TestManager_Up_CustomWorkspaceFolder(t *testing.T) {
	projectDir, _, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "workspaceFolder": "/custom/workspace"}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	meta, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta.WorkspaceDir != "/custom/workspace" {
		t.Errorf("workspaceDir = %q, want %q", meta.WorkspaceDir, "/custom/workspace")
	}
}

func TestManager_Up_SourceDir(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	sourceDir := t.TempDir()

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	_, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		SourceDir:  sourceDir,
		Out:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}
	// The bind mounts should use sourceDir, not projectDir.
	binds := mock.createCalls[0].Binds
	foundSource := false
	for _, b := range binds {
		if strings.HasPrefix(b, sourceDir+":") {
			foundSource = true
			break
		}
	}
	if !foundSource {
		t.Errorf("expected sourceDir %q in binds, got %v", sourceDir, binds)
	}
}

func TestManager_Up_ExistingRunning(t *testing.T) {
	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)

	containerName := ContainerName(projectDir)
	// Mock that returns existing running container in list.
	existingMock := &existingContainerMock{
		mockDockerClient: &mockDockerClient{
			imageInspectErr:    fmt.Errorf("not found"),
			imageInspectResult: ImageInspectResponse{ID: "sha256:pulled"},
			createID:           "container-abc123",
			inspectState:       ContainerState{Running: true},
		},
		containers: []ContainerSummary{{
			ID:     "existing-id",
			Names:  []string{"/" + containerName},
			Image:  "ubuntu:22.04",
			State:  "running",
			Labels: map[string]string{LabelManaged: "true"},
		}},
	}

	mgr := &Manager{Docker: existingMock, Logger: testLogger()}
	var buf bytes.Buffer
	meta, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusRunning {
		t.Errorf("status = %q, want %q", meta.Status, StatusRunning)
	}
	if !strings.Contains(buf.String(), "already running") {
		t.Errorf("expected 'already running' in output: %s", buf.String())
	}
}

func TestManager_Up_StoppedSameConfig(t *testing.T) {
	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)

	containerName := ContainerName(projectDir)
	configData, _ := os.ReadFile(filepath.Join(projectDir, ".devcontainer", "devcontainer.json"))
	hash := ConfigHash(configData)

	existingMock := &existingContainerMock{
		mockDockerClient: &mockDockerClient{
			imageInspectErr:    fmt.Errorf("not found"),
			imageInspectResult: ImageInspectResponse{ID: "sha256:pulled"},
			createID:           "container-abc123",
			inspectState:       ContainerState{Running: true},
		},
		containers: []ContainerSummary{{
			ID:    "stopped-id",
			Names: []string{"/" + containerName},
			Image: "ubuntu:22.04",
			State: "exited",
			Labels: map[string]string{
				LabelManaged:    "true",
				LabelConfigHash: hash,
			},
		}},
	}

	mgr := &Manager{Docker: existingMock, Logger: testLogger()}
	var buf bytes.Buffer
	meta, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusRunning {
		t.Errorf("status = %q, want %q", meta.Status, StatusRunning)
	}
	if !strings.Contains(buf.String(), "Restarting stopped") {
		t.Errorf("expected 'Restarting stopped' in output: %s", buf.String())
	}
}

// existingContainerMock wraps mockDockerClient to return a pre-configured
// container list.
type existingContainerMock struct {
	*mockDockerClient
	containers []ContainerSummary
}

func (m *existingContainerMock) ContainerList(_ context.Context, _ ContainerListOptions) ([]ContainerSummary, error) {
	return m.containers, nil
}

func TestParseRunArgs_Empty(t *testing.T) {
	opts := &ContainerCreateOptions{}
	ParseRunArgs(nil, opts, testLogger())
	if opts.Privileged {
		t.Error("Privileged should be false for empty args")
	}
	if opts.NetworkMode != "" {
		t.Errorf("NetworkMode should be empty, got %q", opts.NetworkMode)
	}
}

func TestNeedsValue(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"--add-host", true},
		{"--cap-add", true},
		{"--security-opt", true},
		{"--network", true},
		{"--privileged", false},
		{"--unknown", false},
	}
	for _, tt := range tests {
		got := needsValue(tt.key)
		if got != tt.want {
			t.Errorf("needsValue(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}
