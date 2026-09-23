package cmddaemon

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/devcontainer"
)

// tarArchive builds a minimal tar stream so a fake CopyFromContainer can
// return something CopyTranscript will actually extract.
func tarArchive(entries map[string]string) io.ReadCloser {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, contents := range entries {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(contents)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(contents))
	}
	_ = tw.Close()
	return io.NopCloser(&buf)
}

// pruneDockerMock implements devcontainer.DockerClient with just enough
// behaviour to drive pruneExitedAgentContainersWith and record, in order,
// whether a transcript copy preceded the container remove — the ordering the
// preservation-before-destroy fix depends on (SC-5248).
type pruneDockerMock struct {
	containers []devcontainer.ContainerSummary

	mu       sync.Mutex
	calls    []string // ordered "copy"/"remove"
	removed  []string
	copiedID []string
}

func (m *pruneDockerMock) ContainerList(_ context.Context, _ devcontainer.ContainerListOptions) ([]devcontainer.ContainerSummary, error) {
	return m.containers, nil
}

func (m *pruneDockerMock) ContainerRemove(_ context.Context, id string, _ devcontainer.ContainerRemoveOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "remove")
	m.removed = append(m.removed, id)
	return nil
}

func (m *pruneDockerMock) CopyFromContainer(_ context.Context, id, _ string) (io.ReadCloser, error) {
	m.mu.Lock()
	m.calls = append(m.calls, "copy")
	m.copiedID = append(m.copiedID, id)
	m.mu.Unlock()
	return tarArchive(map[string]string{"projects/p/s.jsonl": "TRANSCRIPT"}), nil
}

// The rest of devcontainer.DockerClient is unused by the prune path.
func (m *pruneDockerMock) ImageBuild(_ context.Context, _ io.Reader, _ devcontainer.ImageBuildOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (m *pruneDockerMock) ImagePull(_ context.Context, _ string, _ devcontainer.ImagePullOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (m *pruneDockerMock) ImageInspect(_ context.Context, _ string) (devcontainer.ImageInspectResponse, error) {
	return devcontainer.ImageInspectResponse{}, nil
}
func (m *pruneDockerMock) ImageList(_ context.Context, _ devcontainer.ImageListOptions) ([]devcontainer.ImageSummary, error) {
	return nil, nil
}
func (m *pruneDockerMock) ContainerCreate(_ context.Context, _ devcontainer.ContainerCreateOptions) (string, error) {
	return "", nil
}
func (m *pruneDockerMock) ContainerStart(_ context.Context, _ string) error        { return nil }
func (m *pruneDockerMock) ContainerStop(_ context.Context, _ string, _ *int) error { return nil }
func (m *pruneDockerMock) ContainerInspect(_ context.Context, _ string) (devcontainer.ContainerInspectResponse, error) {
	return devcontainer.ContainerInspectResponse{}, nil
}
func (m *pruneDockerMock) ContainerLogs(_ context.Context, _ string, _ devcontainer.LogsOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (m *pruneDockerMock) ContainerCommit(_ context.Context, _ string, _ string, _ map[string]string) (string, error) {
	return "", nil
}
func (m *pruneDockerMock) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}
func (m *pruneDockerMock) ExecCreate(_ context.Context, _ string, _ []string, _ devcontainer.ExecOptions) (string, error) {
	return "", nil
}
func (m *pruneDockerMock) ExecAttach(_ context.Context, _ string) (devcontainer.ExecAttachResponse, error) {
	return devcontainer.ExecAttachResponse{Reader: strings.NewReader(""), Conn: io.NopCloser(strings.NewReader(""))}, nil
}
func (m *pruneDockerMock) ExecInspect(_ context.Context, _ string) (devcontainer.ExecInspectResponse, error) {
	return devcontainer.ExecInspectResponse{}, nil
}
func (m *pruneDockerMock) Close() error { return nil }

// A container the prune removes is debris precisely because no stop path
// reached it — so it is this prune's job to preserve the transcript before
// destroying the container, exactly as every other remove path does. Without
// this, a crashed run's transcript is destroyed with no copy ever made
// (SC-5248 review finding).
func TestPruneExitedAgentContainersWith_PreservesTranscriptBeforeRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	exe, err := agent.NewExecution(agent.LaunchRecord{ID: "e1", Agent: "board-1-planning", StartedAt: time.Now()})
	require.NoError(t, err)
	require.NoError(t, agent.WriteMeta(agent.Meta{
		Name: "board-1-planning", ContainerID: "cid-1", RemoteUser: "vscode",
		CreatedAt: time.Now(), ExecutionID: exe.Launch.ID, Status: agent.StatusStopped,
	}))

	docker := &pruneDockerMock{containers: []devcontainer.ContainerSummary{
		{ID: "cid-1", Names: []string{"/" + agent.ContainerPrefix + "board-1-planning"}, State: "exited"},
	}}

	n, err := pruneExitedAgentContainersWith(context.Background(), docker)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	require.Equal(t, []string{"copy", "remove"}, docker.calls, "transcript must be preserved before the container is removed")

	data, err := os.ReadFile(exe.TranscriptDir() + "/projects/p/s.jsonl")
	require.NoError(t, err)
	assert.Equal(t, "TRANSCRIPT", string(data))
}

// A container with no meta at all (already deleted, or never one of ours to
// track) has nothing to preserve; the prune must still remove it rather than
// getting stuck on a missing record.
func TestPruneExitedAgentContainersWith_NoMetaStillRemoves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	docker := &pruneDockerMock{containers: []devcontainer.ContainerSummary{
		{ID: "cid-2", Names: []string{"/" + agent.ContainerPrefix + "board-2-planning"}, State: "exited"},
	}}

	n, err := pruneExitedAgentContainersWith(context.Background(), docker)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"remove"}, docker.calls, "no meta means nothing to copy, but the container must still be removed")
}
