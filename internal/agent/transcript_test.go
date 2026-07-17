package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/devcontainer"
)

// tarArchive builds a tar archive from name->contents entries.
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

type copyMock struct {
	mockDockerClient
	archive func() io.ReadCloser

	mu    sync.Mutex
	calls []string // ordered record of "copy"/"remove"
}

func (m *copyMock) CopyFromContainer(_ context.Context, _, _ string) (io.ReadCloser, error) {
	m.mu.Lock()
	m.calls = append(m.calls, "copy")
	m.mu.Unlock()
	return m.archive(), nil
}

func (m *copyMock) ContainerRemove(_ context.Context, _ string, _ devcontainer.ContainerRemoveOptions) error {
	m.mu.Lock()
	m.calls = append(m.calls, "remove")
	m.mu.Unlock()
	return nil
}

func TestContainerTranscriptPath(t *testing.T) {
	if got := containerTranscriptPath("vscode"); got != "/home/vscode/.claude/projects" {
		t.Fatalf("got %q", got)
	}
	if got := containerTranscriptPath(""); got != "/root/.claude/projects" {
		t.Fatalf("empty user got %q", got)
	}
	if got := containerTranscriptPath("root"); got != "/root/.claude/projects" {
		t.Fatalf("root got %q", got)
	}
}

func TestCopyTranscript_ExtractsTar(t *testing.T) {
	dest := t.TempDir()
	docker := &copyMock{archive: func() io.ReadCloser {
		return tarArchive(map[string]string{"projects/p/s.jsonl": "SESSION-DATA"})
	}}
	if err := CopyTranscript(context.Background(), docker, "cid", "vscode", dest); err != nil {
		t.Fatalf("CopyTranscript: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "projects", "p", "s.jsonl"))
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if string(data) != "SESSION-DATA" {
		t.Fatalf("contents = %q", string(data))
	}
}

func TestCopyTranscript_RejectsTraversal(t *testing.T) {
	dest := t.TempDir()
	docker := &copyMock{archive: func() io.ReadCloser {
		return tarArchive(map[string]string{"../escape.txt": "EVIL"})
	}}
	if err := CopyTranscript(context.Background(), docker, "cid", "vscode", dest); err != nil {
		t.Fatalf("CopyTranscript should skip traversal, not error: %v", err)
	}
	// The escaping entry must not be written outside dest.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); err == nil {
		t.Fatal("traversal entry escaped destDir")
	}
}

func TestStopLocked_CopiesTranscriptBeforeRemove(t *testing.T) {
	tmp := withLogRoot(t)
	t.Setenv("HOME", tmp)

	exe, err := NewExecution(LaunchRecord{ID: "e1", Agent: "s1", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	meta := Meta{
		Name: "s1", ContainerID: "cid", RemoteUser: "vscode",
		Status: StatusRunning, CreatedAt: time.Now(), ExecutionID: exe.Launch.ID,
	}
	if err := WriteMeta(meta); err != nil {
		t.Fatal(err)
	}
	docker := &copyMock{archive: func() io.ReadCloser {
		return tarArchive(map[string]string{"projects/p/s.jsonl": "X"})
	}}
	mgr := &Manager{Docker: docker}
	if err := mgr.Stop(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if len(docker.calls) < 2 || docker.calls[0] != "copy" || docker.calls[1] != "remove" {
		t.Fatalf("copy must precede remove, got %v", docker.calls)
	}
	var oc OutcomeRecord
	if err := readJSONFile(filepath.Join(exe.Dir(), "outcome.json"), &oc); err != nil {
		t.Fatalf("outcome not written: %v", err)
	}
	if oc.Reason != "completed" {
		t.Fatalf("reason = %q, want completed", oc.Reason)
	}
}

func TestPreserveExecutionArtifacts_CopiesTranscriptAndOutcome(t *testing.T) {
	withLogRoot(t)

	exe, err := NewExecution(LaunchRecord{ID: "e9", Agent: "d1", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	meta := Meta{
		Name: "d1", ContainerID: "cid", RemoteUser: "vscode",
		CreatedAt: time.Now(), ExecutionID: exe.Launch.ID,
	}
	docker := &copyMock{archive: func() io.ReadCloser {
		return tarArchive(map[string]string{"projects/p/s.jsonl": "DECOM-DATA"})
	}}
	PreserveExecutionArtifacts(context.Background(), docker, meta, "reaped")

	data, err := os.ReadFile(filepath.Join(exe.TranscriptDir(), "projects", "p", "s.jsonl"))
	if err != nil {
		t.Fatalf("transcript not copied: %v", err)
	}
	if string(data) != "DECOM-DATA" {
		t.Fatalf("contents = %q", string(data))
	}
	var oc OutcomeRecord
	if err := readJSONFile(filepath.Join(exe.Dir(), "outcome.json"), &oc); err != nil {
		t.Fatalf("outcome not written: %v", err)
	}
	if oc.Reason != "reaped" {
		t.Fatalf("reason = %q, want reaped", oc.Reason)
	}
}

func TestPreserveExecutionArtifacts_NoExecutionIsNoop(t *testing.T) {
	withLogRoot(t)

	docker := &copyMock{archive: func() io.ReadCloser { return tarArchive(nil) }}
	// No execution dir exists for this agent; preservation must be a silent no-op.
	PreserveExecutionArtifacts(context.Background(), docker, Meta{Name: "ghost", ContainerID: "cid"}, "reaped")
	if len(docker.calls) != 0 {
		t.Fatalf("expected no docker calls, got %v", docker.calls)
	}
}

func TestStopLocked_ReapedReason(t *testing.T) {
	tmp := withLogRoot(t)
	t.Setenv("HOME", tmp)

	exe, err := NewExecution(LaunchRecord{ID: "e2", Agent: "s2", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// A reaped agent is marked StatusFailed before teardown.
	meta := Meta{
		Name: "s2", ContainerID: "cid", RemoteUser: "vscode",
		Status: StatusFailed, CreatedAt: time.Now(), ExecutionID: exe.Launch.ID,
	}
	if err := WriteMeta(meta); err != nil {
		t.Fatal(err)
	}
	docker := &copyMock{archive: func() io.ReadCloser { return tarArchive(nil) }}
	mgr := &Manager{Docker: docker}
	if err := mgr.Stop(context.Background(), "s2"); err != nil {
		t.Fatal(err)
	}
	var oc OutcomeRecord
	if err := readJSONFile(filepath.Join(exe.Dir(), "outcome.json"), &oc); err != nil {
		t.Fatalf("outcome not written: %v", err)
	}
	if oc.Reason != "reaped" {
		t.Fatalf("reason = %q, want reaped", oc.Reason)
	}
}
