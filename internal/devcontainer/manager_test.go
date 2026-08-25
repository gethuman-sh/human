package devcontainer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/daemon"
)

// must returns v unchanged, failing the test when it is nil. Centralizing the
// guard keeps call sites free of check-then-dereference sequences, which
// golangci-lint's embedded staticcheck misreads as SA5011 (it loses t.Fatal's
// no-return fact).
func must[T any](t *testing.T, v *T, msg string) *T {
	t.Helper()
	if v == nil {
		t.Fatal(msg)
	}
	return v
}

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

	// Every launch now resolves a linux human before it creates anything, so a
	// test project carries one (SC-4631).
	writeFakeLinuxHuman(t, projectDir, runtime.GOARCH)

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
	meta = must(t, meta, "expected non-nil meta")
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
		want  string // Mount.String(), or "" when the mount is not expressible
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
		// Non-bind mount type (volume) should not be expressible.
		{"source=myvolume,target=/data,type=volume", ""},
		// Missing source or target.
		{"target=/container/path,type=bind", ""},
		{"source=/host/path,type=bind", ""},
		// No type specified (defaults to bind).
		{"source=/host/path,target=/container/path", "/host/path:/container/path"},
	}
	for _, tt := range tests {
		m, ok := parseMountString(tt.input)
		got := ""
		if ok {
			got = m.String()
		}
		if got != tt.want {
			t.Errorf("parseMountString(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseMountString_WithSpaces(t *testing.T) {
	m, ok := parseMountString("source=/host/path , target=/container/path , type=bind")
	if !ok || m.String() != "/host/path:/container/path" {
		t.Errorf("parseMountString with spaces = %q (ok=%v), want %q", m.String(), ok, "/host/path:/container/path")
	}
}

func TestDedupeMounts(t *testing.T) {
	got := dedupeMounts([]Mount{
		Bind("/first", "/target-a"),
		Bind("/second", "/target-b"),
		Bind("/third", "/target-a"), // duplicate target, should replace /first
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped mounts, got %d: %v", len(got), got)
	}
	foundA := false
	for _, m := range got {
		if m.Target == "/target-a" {
			foundA = true
			if m.Source != "/third" {
				t.Errorf("expected /third to win /target-a, got %q", m.Source)
			}
		}
	}
	if !foundA {
		t.Error("missing /target-a entry")
	}
}

func TestDedupeMounts_NoConflicts(t *testing.T) {
	got := dedupeMounts([]Mount{Bind("/a", "/x"), Bind("/b", "/y"), Bind("/c", "/z")})
	if len(got) != 3 {
		t.Errorf("expected 3 mounts, got %d", len(got))
	}
}

func TestDedupeMounts_WithOptions(t *testing.T) {
	got := dedupeMounts([]Mount{
		Bind("/first", "/target", "ro"),
		Bind("/second", "/target", "rw"), // same target, should replace
	})
	if len(got) != 1 {
		t.Fatalf("expected 1 mount, got %d: %v", len(got), got)
	}
	if got[0].String() != "/second:/target:rw" {
		t.Errorf("expected /second:/target:rw, got %q", got[0].String())
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
	binds := bindStrings(mock.createCalls[0].Binds)
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

// Regression for SC-482: a worktree workspace's .git is a FILE pointing at the
// parent repo's .git by absolute host path. Binding only the worktree leaves
// every in-container git command dying with "not a git repository" — the
// parent .git must be bound at its host-identical path alongside.
func TestManager_Up_WorktreeGitDirBind(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	gitDir := filepath.Join(projectDir, ".git")

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	_, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		SourceDir:  t.TempDir(), // stands in for the private worktree
		GitDir:     gitDir,
		Out:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}
	want := gitDir + ":" + gitDir
	if slices.Contains(bindStrings(mock.createCalls[0].Binds), want) {
		return
	}
	t.Errorf("missing parent-repo git bind %q in binds: %v", want, bindStrings(mock.createCalls[0].Binds))
}

// Without a GitDir (shared-checkout mount, non-git workspace) no extra bind
// appears — the workspace's own .git directory travels with the source mount.
func TestManager_Up_NoGitDirNoExtraBind(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04"}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	_, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindStrings(mock.createCalls[0].Binds) {
		if strings.Contains(b, ".git:") {
			t.Errorf("unexpected .git bind %q without GitDir", b)
		}
	}
}

func TestManager_Up_WithCACert(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)

	// Create a real PEM CA cert in the test HOME; the mount is now gated on
	// the file being a PEM-parseable certificate.
	home := os.Getenv("HOME")
	writeTestCA(t, filepath.Join(home, ".human"))

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
	binds := bindStrings(mock.createCalls[0].Binds)
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

func TestManager_Up_CACertIsDirectory_NoMount(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)

	// Reproduce the broken host state: Docker auto-creates the missing bind
	// source as an empty directory. A directory must never be mounted as the
	// ca.crt PEM file.
	home := os.Getenv("HOME")
	humanDir := filepath.Join(home, ".human")
	if err := os.MkdirAll(filepath.Join(humanDir, "ca.crt"), 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	if _, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &bytes.Buffer{},
	}); err != nil {
		t.Fatal(err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}
	for _, b := range bindStrings(mock.createCalls[0].Binds) {
		if strings.Contains(b, "/.human/ca.crt") {
			t.Errorf("ca.crt directory must not be mounted, but found bind: %q in %v", b, bindStrings(mock.createCalls[0].Binds))
		}
	}
}

func TestManager_Up_CACertEmpty_NoMount(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)

	home := os.Getenv("HOME")
	humanDir := filepath.Join(home, ".human")
	if err := os.MkdirAll(humanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A zero-byte / non-PEM file would make Node's PEM parse fail; it must not
	// be mounted either.
	if err := os.WriteFile(filepath.Join(humanDir, "ca.crt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	if _, err := mgr.Up(context.Background(), UpOptions{
		ProjectDir: projectDir,
		Out:        &bytes.Buffer{},
	}); err != nil {
		t.Fatal(err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}
	for _, b := range bindStrings(mock.createCalls[0].Binds) {
		if strings.Contains(b, "/.human/ca.crt") {
			t.Errorf("empty ca.crt must not be mounted, but found bind: %q in %v", b, bindStrings(mock.createCalls[0].Binds))
		}
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
	binds := bindStrings(mock.createCalls[0].Binds)
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
	binds := bindStrings(mock.createCalls[0].Binds)
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
	configData, _ := os.ReadFile(filepath.Join(projectDir, ".devcontainer", "devcontainer.json"))
	hash := ConfigHash(configData)
	// Mock that returns existing running container in list, labelled with the
	// current config hash so it is reused as-is.
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
			Labels: map[string]string{LabelManaged: "true", LabelConfigHash: hash},
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

func TestManager_handleExisting_RunningConfigChanged(t *testing.T) {
	// A running container whose stored config hash no longer matches the
	// current config must be removed and signalled for rebuild — not silently
	// reused. This is the core of the running-container rebuild fix.
	mock := &mockDockerClient{}
	mgr := &Manager{Docker: mock, Logger: testLogger()}

	existing := ContainerSummary{
		ID:     "existing-id",
		State:  "running",
		Labels: map[string]string{LabelManaged: "true", LabelConfigHash: "stale-hash"},
	}
	cfg := &DevcontainerConfig{Image: "ubuntu:22.04"}

	var buf bytes.Buffer
	_, err := mgr.handleExisting(context.Background(), existing, cfg, "current-hash", "human-test", t.TempDir(), &buf)
	if err == nil {
		t.Fatal("expected 'config changed' error for stale running container")
	}
	if !strings.Contains(err.Error(), "config changed") {
		t.Errorf("error = %v, want 'config changed'", err)
	}
	found := false
	for _, id := range mock.removeCalls {
		if id == "existing-id" {
			found = true
		}
	}
	if !found {
		t.Error("expected stale running container to be removed for rebuild")
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

// neverStartedMock wraps pullThenInspectMock (whose ImageInspect fails once
// then succeeds, as EnsureImage's pull-then-inspect flow requires) to also
// return a pre-configured container list, for a rebuild-after-"created" test
// that must reach createFresh instead of stalling on a permanently failing
// image inspect.
type neverStartedMock struct {
	*pullThenInspectMock
	containers []ContainerSummary
}

func (m *neverStartedMock) ContainerList(_ context.Context, _ ContainerListOptions) ([]ContainerSummary, error) {
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

// SC-783: project-declared cache volumes become named-volume binds so
// consecutive runs build warm; the volume roots are chowned for a non-root
// remote user because Docker creates fresh named volumes root-owned.
func TestManager_Up_CacheVolumes(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)
	writeCachesConfig(t, projectDir, `caches:
  - name: go-build
    path: /home/vscode/.cache/go-build
  - name: go-mod
    path: /go/pkg/mod
`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}

	binds := bindStrings(mock.createCalls[0].Binds)
	for _, want := range []string{
		"human-cache-go-build:/home/vscode/.cache/go-build",
		"human-cache-go-mod:/go/pkg/mod",
	} {
		if !slices.Contains(binds, want) {
			t.Errorf("missing %q in binds: %v", want, binds)
		}
	}

	// Ownership fix: exactly one mkdir and one chown exec as root.
	var mkdirAsRoot, chownAsRoot bool
	for _, c := range mock.execCalls {
		if c.Opts.User != "root" {
			continue
		}
		if len(c.Cmd) > 0 && c.Cmd[0] == "mkdir" && slices.Contains(c.Cmd, "/go/pkg/mod") {
			mkdirAsRoot = true
		}
		if len(c.Cmd) > 0 && c.Cmd[0] == "chown" && slices.Contains(c.Cmd, "vscode") {
			chownAsRoot = true
		}
	}
	if !mkdirAsRoot || !chownAsRoot {
		t.Errorf("expected root mkdir+chown for cache roots, got execs: %+v", mock.execCalls)
	}
}

func TestManager_Up_CacheVolumes_RootUserSkipsChown(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	writeCachesConfig(t, projectDir, "caches:\n  - name: go-mod\n    path: /go/pkg/mod\n")

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(bindStrings(mock.createCalls[0].Binds), "human-cache-go-mod:/go/pkg/mod") {
		t.Fatalf("volume bind missing: %v", bindStrings(mock.createCalls[0].Binds))
	}
	for _, c := range mock.execCalls {
		if len(c.Cmd) > 0 && c.Cmd[0] == "chown" {
			t.Errorf("root remoteUser must not trigger a chown exec: %+v", c)
		}
	}
}

func TestManager_Up_CacheVolumes_InvalidSkippedWithWarning(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	writeCachesConfig(t, projectDir, `caches:
  - name: ../escape
    path: /data
  - name: relative
    path: data
  - name: good
    path: /data
`)

	var out bytes.Buffer
	mgr := &Manager{Docker: docker, Logger: testLogger()}
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	binds := bindStrings(mock.createCalls[0].Binds)
	if !slices.Contains(binds, "human-cache-good:/data") {
		t.Errorf("valid entry missing: %v", binds)
	}
	for _, b := range binds {
		if strings.Contains(b, "escape") || strings.Contains(b, "relative") {
			t.Errorf("invalid entry reached binds: %v", binds)
		}
	}
	if !strings.Contains(out.String(), "ignoring invalid cache volume") {
		t.Errorf("expected warning, got: %s", out.String())
	}
}

func TestManager_Up_NoCachesNoExtraExecs(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range mock.execCalls {
		if len(c.Cmd) > 0 && (c.Cmd[0] == "chown" || c.Cmd[0] == "mkdir") {
			t.Errorf("no caches declared but ownership exec ran: %+v", c)
		}
	}
	for _, b := range bindStrings(mock.createCalls[0].Binds) {
		if strings.HasPrefix(b, "human-cache-") {
			t.Errorf("unexpected cache bind: %v", b)
		}
	}
}

// writeCachesConfig drops a .humanconfig.yaml with the given content into the
// test project dir.
func writeCachesConfig(t *testing.T, projectDir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(projectDir, ".humanconfig.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A project that declares the same --add-host the manager already injects must
// not end up with the host twice: Docker writes one /etc/hosts line per entry,
// and a duplicate line makes `getent hosts` print two — which is how the
// container's proxy redirect silently stopped being installed.
func TestManager_Up_DoesNotDuplicateInjectedExtraHost(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{
		"name": "test", "image": "ubuntu:22.04", "remoteUser": "vscode",
		"runArgs": ["--add-host=host.docker.internal:host-gateway"]
	}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	var buf bytes.Buffer
	if _, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf}); err != nil {
		t.Fatal(err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}
	hosts := mock.createCalls[0].ExtraHosts
	if len(hosts) != 1 || hosts[0] != "host.docker.internal:host-gateway" {
		t.Errorf("ExtraHosts = %v, want exactly one host.docker.internal entry", hosts)
	}
}

// Agent launches build a Manager with no Logger, and a zero zerolog.Logger is
// disabled — so a failed hook must still be announced on the caller's writer,
// or a container comes up incomplete (no proxy redirect, no CA trust) while the
// output reads as a clean success.
func TestManager_Up_FailedHookIsVisibleWithoutLogger(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{
		"name": "test", "image": "ubuntu:22.04", "remoteUser": "vscode",
		"postStartCommand": "human-proxy-setup"
	}`)
	mock.execExit = 1

	mgr := &Manager{Docker: docker} // no Logger: the agent-launch condition
	var buf bytes.Buffer
	if _, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), "WARNING") || !strings.Contains(buf.String(), "may be incomplete") {
		t.Errorf("a failed lifecycle hook must be reported on the writer: %s", buf.String())
	}
}

// Binding the daemon's own executable into the container (os.Executable() onto
// /usr/local/bin/human) is the SC-4631 defect: on macOS the source is either
// outside Docker Desktop's shared paths (silently materialized as an empty
// directory, failing runc init) or a Mach-O binary the linux image cannot
// execute. No bind of any kind belongs in the create options any more.
func TestBuildCreateOptions_NoHostBinaryBindRemains(t *testing.T) {
	m := &Manager{}
	dir := t.TempDir()

	opts := m.buildCreateOptions(&DevcontainerConfig{}, dir, dir, "human-agent-board-SC-1-implementation", "img", "/workspace", "hash", nil, "", nil)

	for _, b := range opts.Binds {
		if b.Target == "/usr/local/bin/human" {
			t.Errorf("buildCreateOptions still binds a host path onto %q: %+v", b.Target, b)
		}
	}
}

// A host with no usable linux/<arch> human must fail before ContainerCreate,
// naming the file to produce — not hand the container a binary that dies at
// runc init or exec format error (SC-4631).
func TestManager_Up_NoLinuxBinary_CreatesNoContainer(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)
	if err := os.Remove(filepath.Join(projectDir, "bin", "human-linux-"+runtime.GOARCH)); err != nil {
		t.Fatal(err)
	}

	mgr := &Manager{
		Docker: docker, Logger: testLogger(),
		hostExecutable: func() (string, error) { return "", fmt.Errorf("no executable") },
	}
	var buf bytes.Buffer
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf})
	if err == nil {
		t.Fatal("expected an error when no linux human binary is available")
	}
	if !strings.Contains(err.Error(), "bin/human-linux-") {
		t.Errorf("error = %v, want it to name the candidate path", err)
	}
	if len(mock.createCalls) != 0 {
		t.Errorf("createCalls = %d, want 0 — no container for an unresolved binary", len(mock.createCalls))
	}
	if len(mock.startCalls) != 0 {
		t.Errorf("startCalls = %d, want 0", len(mock.startCalls))
	}
}

// The resolved binary must reach the container before ContainerStart: the
// image's postStartCommand runs `human`, so a container started with the wrong
// (or no) binary has already failed its hooks by the time the copy would land
// (SC-4631, AD1).
func TestManager_Up_CopiesTheLinuxBinaryBeforeStart(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	var buf bytes.Buffer
	if _, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf}); err != nil {
		t.Fatal(err)
	}

	if len(mock.copyCalls) != 1 {
		t.Fatalf("copyCalls = %d, want 1", len(mock.copyCalls))
	}
	call := mock.copyCalls[0]
	if call.Dst != "/usr/local/bin" {
		t.Errorf("copy dst = %q, want /usr/local/bin", call.Dst)
	}
	if call.EntryName != "human" {
		t.Errorf("copy entry name = %q, want human", call.EntryName)
	}
	if call.EntryMode != 0o755 {
		t.Errorf("copy entry mode = %o, want 0755", call.EntryMode)
	}
	if got := strings.Join(mock.callLog, ","); got != "create,copy,start" {
		t.Errorf("callLog = %q, want create,copy,start", got)
	}
}

// The image, not the host, decides which architecture's binary is needed —
// the launch must resolve human-linux-amd64 on an arm64 daemon host running an
// amd64 image (SC-4631, AD2).
func TestManager_Up_ArchitectureComesFromTheImage(t *testing.T) {
	projectDir, _, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)
	// Remove the default runtime.GOARCH fixture and provide only an amd64 one,
	// so the test proves the arch came from the image and not the host.
	if err := os.Remove(filepath.Join(projectDir, "bin", "human-linux-"+runtime.GOARCH)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	writeFakeLinuxHuman(t, projectDir, "amd64")
	docker.inspectResult.Architecture = "amd64"

	mgr := &Manager{
		Docker: docker, Logger: testLogger(),
		hostExecutable: func() (string, error) { return "", fmt.Errorf("no executable") },
	}
	var buf bytes.Buffer
	if _, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf}); err != nil {
		t.Fatalf("Up: %v", err)
	}
}

// A copy that fails must not leave a container the postStartCommand will run
// against a missing/incomplete binary — remove it and surface the error
// instead (SC-4631).
func TestManager_Up_CopyFailureRemovesTheContainer(t *testing.T) {
	projectDir, mock, docker := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)
	mock.copyErr = fmt.Errorf("no such directory")

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	var buf bytes.Buffer
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf})
	if err == nil {
		t.Fatal("expected an error when the copy fails")
	}
	if len(mock.startCalls) != 0 {
		t.Errorf("startCalls = %d, want 0 — a container with no binary must never start", len(mock.startCalls))
	}
	found := false
	for _, id := range mock.removeCalls {
		if id == mock.createID {
			found = true
		}
	}
	if !found {
		t.Errorf("removeCalls = %v, want the created container id removed", mock.removeCalls)
	}
}

// A container in state "created" was never started — every container the
// host-binary bind broke is stuck exactly there, with its config hash still
// matching, so it must be rebuilt rather than "restarted" into the same dead
// state (SC-4631, AD6).
func TestManager_Up_NeverStartedContainerIsRebuiltNotRestarted(t *testing.T) {
	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04", "remoteUser": "vscode"}`)

	containerName := ContainerName(projectDir)
	configData, err := os.ReadFile(filepath.Join(projectDir, ".devcontainer", "devcontainer.json"))
	if err != nil {
		t.Fatal(err)
	}
	hash := ConfigHash(configData)

	// The rebuild path re-runs EnsureImage, which pulls then inspects — so the
	// image-inspect mock must succeed on the second call, exactly like a fresh
	// TestManager_Up_NewContainer run, not fail every time.
	callCount := 0
	pullMock := &pullThenInspectMock{
		mockDockerClient: &mockDockerClient{
			imageInspectErr: fmt.Errorf("not found"),
			createID:        "container-fresh",
			inspectState:    ContainerState{Running: true},
		},
		inspectCallCount: &callCount,
		inspectErr:       fmt.Errorf("not found"),
		inspectResult:    ImageInspectResponse{ID: "sha256:pulled", Tags: []string{"ubuntu:22.04"}},
	}
	existingMock := &neverStartedMock{
		pullThenInspectMock: pullMock,
		containers: []ContainerSummary{{
			ID:     "never-started-id",
			Names:  []string{"/" + containerName},
			Image:  "ubuntu:22.04",
			State:  "created",
			Labels: map[string]string{LabelManaged: "true", LabelConfigHash: hash},
		}},
	}

	mgr := &Manager{Docker: existingMock, Logger: testLogger()}
	var buf bytes.Buffer
	meta, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf})
	if err != nil {
		t.Fatal(err)
	}
	if meta.ContainerID != "container-fresh" {
		t.Errorf("ContainerID = %q, want the freshly created container", meta.ContainerID)
	}
	removedOld := false
	for _, id := range existingMock.removeCalls {
		if id == "never-started-id" {
			removedOld = true
		}
	}
	if !removedOld {
		t.Errorf("removeCalls = %v, want the never-started container removed", existingMock.removeCalls)
	}
	for _, id := range existingMock.startCalls {
		if id == "never-started-id" {
			t.Errorf("ContainerStart was called on the never-started container %q — it must be rebuilt, not restarted", id)
		}
	}
}
