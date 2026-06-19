package devcontainer

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestBuildWithFeatures_BakesContainerEnv asserts that feature containerEnv is
// passed to ContainerCommit so it is baked into the committed image — the fix
// that lets node/go end up on PATH for containers created from the image.
func TestBuildWithFeatures_BakesContainerEnv(t *testing.T) {
	mock := &mockDockerClient{createID: "temp-cid", commitID: "sha256:withenv"}
	puller := &mockFeaturePuller{
		tarData: buildFeatureTar(t, "node", "1.0.0"),
		metaByRef: map[string]*FeatureMeta{
			"ghcr.io/devcontainers/features/node:1": {ID: "node", ContainerEnv: map[string]string{
				"PATH":    "/usr/local/share/nvm/current/bin:${PATH}",
				"NVM_DIR": "/usr/local/share/nvm",
			}},
		},
	}
	builder := &ImageBuilder{Docker: mock, Logger: testLogger(), Puller: puller}

	cfg := &DevcontainerConfig{
		Image:    "mcr.microsoft.com/devcontainers/base:ubuntu",
		Features: map[string]interface{}{"ghcr.io/devcontainers/features/node:1": map[string]interface{}{}},
	}
	id, _, err := builder.buildWithFeatures(context.Background(), cfg, "base:ref", "human-dc-test:hash", &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if id != "sha256:withenv" {
		t.Errorf("commit id = %q, want sha256:withenv", id)
	}
	if mock.commitEnv["NVM_DIR"] != "/usr/local/share/nvm" {
		t.Errorf("commit env NVM_DIR = %q, want it baked in", mock.commitEnv["NVM_DIR"])
	}
	if !strings.Contains(mock.commitEnv["PATH"], "/usr/local/share/nvm/current/bin") {
		t.Errorf("commit env PATH = %q, want nvm bin path baked in", mock.commitEnv["PATH"])
	}
}

func TestEnsureImage_Cached(t *testing.T) {
	mock := &mockDockerClient{
		imageInspectResult: ImageInspectResponse{ID: "sha256:cached", Tags: []string{"human-dc-test:abc123abc123"}},
	}
	builder := &ImageBuilder{Docker: mock, Logger: testLogger()}

	id, name, err := builder.EnsureImage(context.Background(), &DevcontainerConfig{Image: "ubuntu"}, "/tmp/test", "abc123abc123def456", false, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if id != "sha256:cached" {
		t.Errorf("expected cached image ID, got %q", id)
	}
	if !strings.HasPrefix(name, "human-dc-") {
		t.Errorf("unexpected image name: %q", name)
	}
	// Should not have pulled.
	if len(mock.pullCalls) != 0 {
		t.Errorf("should not pull when cached, got %d pull calls", len(mock.pullCalls))
	}
}

func TestEnsureImage_PullOnMiss(t *testing.T) {
	mock := &mockDockerClient{
		imageInspectErr:    fmt.Errorf("not found"),
		imageInspectResult: ImageInspectResponse{ID: "sha256:pulled"},
	}
	// After pull, ImageInspect should succeed for the ref.
	callCount := 0
	origInspect := mock.imageInspectErr
	mock2 := &pullThenInspectMock{
		mockDockerClient: mock,
		inspectCallCount: &callCount,
		inspectErr:       origInspect,
		inspectResult:    ImageInspectResponse{ID: "sha256:pulled", Tags: []string{"ubuntu"}},
	}

	builder := &ImageBuilder{Docker: mock2, Logger: testLogger()}
	_, _, err := builder.EnsureImage(context.Background(), &DevcontainerConfig{Image: "ubuntu"}, "/tmp/test", "abc123abc123def456", false, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.pullCalls) != 1 {
		t.Errorf("expected 1 pull call, got %d", len(mock.pullCalls))
	}
}

func TestEnsureImage_ForcedRebuild(t *testing.T) {
	mock := &mockDockerClient{
		imageInspectResult: ImageInspectResponse{ID: "sha256:cached"},
	}
	// Even with cached image, rebuild=true should pull.
	callCount := 0
	mock2 := &pullThenInspectMock{
		mockDockerClient: mock,
		inspectCallCount: &callCount,
		inspectResult:    ImageInspectResponse{ID: "sha256:fresh"},
	}

	builder := &ImageBuilder{Docker: mock2, Logger: testLogger()}
	id, _, err := builder.EnsureImage(context.Background(), &DevcontainerConfig{Image: "ubuntu"}, "/tmp/test", "abc123abc123", true, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.pullCalls) != 1 {
		t.Errorf("expected pull on rebuild, got %d calls", len(mock.pullCalls))
	}
	if id != "sha256:fresh" {
		t.Errorf("expected fresh image ID, got %q", id)
	}
}

func TestEnsureImage_NoImageOrBuild(t *testing.T) {
	mock := &mockDockerClient{
		imageInspectErr: fmt.Errorf("not found"),
	}
	builder := &ImageBuilder{Docker: mock, Logger: testLogger()}
	_, _, err := builder.EnsureImage(context.Background(), &DevcontainerConfig{}, "/tmp/test", "hash", false, &strings.Builder{})
	if err == nil {
		t.Error("expected error when neither image nor build specified")
	}
}

// pullThenInspectMock wraps mockDockerClient to simulate: first inspect fails
// (cache miss), then succeeds after pull.
type pullThenInspectMock struct {
	*mockDockerClient
	inspectCallCount *int
	inspectErr       error
	inspectResult    ImageInspectResponse
}

func (m *pullThenInspectMock) ImageInspect(_ context.Context, _ string) (ImageInspectResponse, error) {
	*m.inspectCallCount++
	if *m.inspectCallCount == 1 && m.inspectErr != nil {
		return ImageInspectResponse{}, m.inspectErr
	}
	return m.inspectResult, nil
}

func TestDrainDockerOutput_NoError(t *testing.T) {
	// Docker JSON stream with status messages but no error.
	input := strings.NewReader(`{"status":"Pulling from library/ubuntu"}
{"status":"Digest: sha256:abc123"}
{"status":"Status: Downloaded newer image"}
`)
	err := drainDockerOutput(input)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestDrainDockerOutput_WithError(t *testing.T) {
	input := strings.NewReader(`{"status":"Pulling from library/ubuntu"}
{"error":"pull access denied"}
{"status":"should not matter"}
`)
	err := drainDockerOutput(input)
	if err == nil {
		t.Error("expected error from Docker stream")
	}
}

func TestDrainDockerOutput_EmptyStream(t *testing.T) {
	input := strings.NewReader("")
	err := drainDockerOutput(input)
	if err != nil {
		t.Errorf("expected no error for empty stream, got %v", err)
	}
}

func TestDrainDockerOutput_InvalidJSON(t *testing.T) {
	// Non-JSON lines should be skipped without error.
	input := strings.NewReader("not json at all\n{also not valid\n")
	err := drainDockerOutput(input)
	if err != nil {
		t.Errorf("expected no error for invalid JSON lines, got %v", err)
	}
}

func TestEnsureImage_DockerFileShorthand(t *testing.T) {
	// Test the cfg.DockerFile (non-build) path.
	mock := &mockDockerClient{
		imageInspectErr:    fmt.Errorf("not found"),
		imageInspectResult: ImageInspectResponse{ID: "sha256:built"},
	}
	callCount := 0
	mock2 := &pullThenInspectMock{
		mockDockerClient: mock,
		inspectCallCount: &callCount,
		inspectErr:       fmt.Errorf("not found"),
		inspectResult:    ImageInspectResponse{ID: "sha256:built", Tags: []string{"human-dc-test:abc123"}},
	}

	// Create temp project with a Dockerfile.
	tmp := t.TempDir()
	dcDir := fmt.Sprintf("%s/.devcontainer", tmp)
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fmt.Sprintf("%s/.devcontainer/Dockerfile", tmp), []byte("FROM ubuntu:22.04\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	builder := &ImageBuilder{Docker: mock2, Logger: testLogger()}
	cfg := &DevcontainerConfig{DockerFile: "Dockerfile"}
	_, _, err := builder.EnsureImage(context.Background(), cfg, tmp, "abc123abc123def456", false, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
}
