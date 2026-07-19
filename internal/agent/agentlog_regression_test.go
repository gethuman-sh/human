package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/devcontainer"
)

// stdoutFrame wraps payload in a Docker stdcopy multiplexed stdout frame:
// an 8-byte header (stream=1, then a big-endian uint32 length) followed by the
// payload. This is the exact wire format the detached exec attach emits.
func stdoutFrame(payload string) []byte {
	h := make([]byte, 8)
	h[0] = 1 // stdout stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, []byte(payload)...)
}

// tarWithTranscript builds a tar archive carrying one transcript file so the
// mock CopyFromContainer can stand in for a real container copy-out.
func tarWithTranscript(name, contents string) io.ReadCloser {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(contents))}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte(contents))
	_ = tw.Close()
	return io.NopCloser(&buf)
}

// regMockDocker extends the base mock with a real stdout frame on ExecAttach
// and a real tar archive on CopyFromContainer, so the tee and copy-out paths
// have known bytes to persist.
type regMockDocker struct {
	mockDockerClient
	stdoutPayload string
	transcript    string
}

func (m *regMockDocker) ExecAttach(_ context.Context, _ string) (devcontainer.ExecAttachResponse, error) {
	return devcontainer.ExecAttachResponse{
		Reader: bytes.NewReader(stdoutFrame(m.stdoutPayload)),
		Conn:   io.NopCloser(strings.NewReader("")),
	}, nil
}

func (m *regMockDocker) CopyFromContainer(_ context.Context, _, _ string) (io.ReadCloser, error) {
	return tarWithTranscript("projects/proj/session.jsonl", m.transcript), nil
}

// TestAgentExecution_PersistsOutputAndTranscript is the regression test
// mandated by triage: start an agent through a fake Docker whose exec Reader
// emits known bytes, force-remove the container, then assert the persisted
// execution log contains those stdout bytes and a copied transcript. On the
// pre-fix code the detached output is discarded and the transcript destroyed,
// so this fails; after the fix both are durable on the host.
func TestAgentExecution_PersistsOutputAndTranscript(t *testing.T) {
	tmp := t.TempDir()
	agentLogsDirOverride = tmp
	t.Cleanup(func() { agentLogsDirOverride = "" })

	// Redirect the agents meta dir (AgentsDir reads $HOME) into the temp tree
	// so stopLocked can read the meta we write below without touching real home.
	t.Setenv("HOME", tmp)

	docker := &regMockDocker{
		stdoutPayload: "AGENT-STDOUT-MARKER",
		transcript:    "TRANSCRIPT-MARKER",
	}
	mgr := &Manager{Docker: docker}
	// The tee goroutine creates files asynchronously; without this wait it
	// races the TempDir RemoveAll and cleanup fails with ENOTEMPTY.
	t.Cleanup(mgr.teeWG.Wait)
	ctx := context.Background()

	exe, err := mgr.execClaudeDetached(ctx, "container-xyz", "vscode", "", "", StartOpts{
		Name: "reg", Prompt: "do it", Model: "opus",
	})
	if err != nil {
		t.Fatalf("execClaudeDetached: %v", err)
	}
	if exe == nil {
		t.Fatal("expected non-nil execution")
	}

	// Persist the meta so stopLocked can find the container and execution id.
	meta := Meta{
		Name: "reg", ContainerID: "container-xyz", RemoteUser: "vscode",
		Status: StatusRunning, CreatedAt: time.Now(), ExecutionID: exe.Launch.ID,
	}
	if err := WriteMeta(meta); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	// Wait for the tee goroutine to flush the stdout frame to output.log.
	waitForOutput(t, filepath.Join(tmp, "reg"))

	if err := mgr.Stop(ctx, "reg"); err != nil {
		t.Fatalf("stop: %v", err)
	}

	execs, err := ListExecutions("reg")
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(execs) == 0 {
		t.Fatal("expected at least one execution log")
	}
	dir := execs[0].Dir

	out, err := os.ReadFile(filepath.Join(dir, "output.log"))
	if err != nil {
		t.Fatalf("read output.log: %v", err)
	}
	if !strings.Contains(string(out), "AGENT-STDOUT-MARKER") {
		t.Fatalf("output.log missing stdout marker, got %q", string(out))
	}

	transcript := readTree(t, filepath.Join(dir, "transcript"))
	if !strings.Contains(transcript, "TRANSCRIPT-MARKER") {
		t.Fatalf("transcript missing marker, got %q", transcript)
	}

	if execs[0].Launch.Prompt != "do it" {
		t.Fatalf("launch prompt = %q, want %q", execs[0].Launch.Prompt, "do it")
	}
	if execs[0].Launch.Model != "opus" {
		t.Fatalf("launch model = %q, want opus", execs[0].Launch.Model)
	}
	if !containsArg(execs[0].Launch.Argv, "-p") {
		t.Fatalf("launch argv missing -p: %v", execs[0].Launch.Argv)
	}
}

// waitForOutput polls up to 2s for output.log to appear under some execution
// dir beneath agentDir, so the async tee has time to flush.
func waitForOutput(t *testing.T, agentDir string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(agentDir)
		for _, e := range entries {
			p := filepath.Join(agentDir, e.Name(), "output.log")
			if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readTree(t *testing.T, root string) string {
	t.Helper()
	var sb strings.Builder
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		sb.Write(data)
		return nil
	})
	return sb.String()
}

func containsArg(argv []string, want string) bool {
	return slices.Contains(argv, want)
}
