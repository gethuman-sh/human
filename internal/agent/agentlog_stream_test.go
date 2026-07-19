package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "output.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestStreamOutput_noFollow(t *testing.T) {
	path := writeTempFile(t, "a\nb\nc\n")
	var buf bytes.Buffer
	if err := StreamOutput(context.Background(), &buf, path, false, 0); err != nil {
		t.Fatalf("StreamOutput: %v", err)
	}
	if buf.String() != "a\nb\nc\n" {
		t.Errorf("output = %q, want %q", buf.String(), "a\nb\nc\n")
	}
}

func TestStreamOutput_tailLines(t *testing.T) {
	path := writeTempFile(t, "l1\nl2\nl3\nl4\n")
	var buf bytes.Buffer
	if err := StreamOutput(context.Background(), &buf, path, false, 2); err != nil {
		t.Fatalf("StreamOutput: %v", err)
	}
	if buf.String() != "l3\nl4\n" {
		t.Errorf("output = %q, want %q", buf.String(), "l3\nl4\n")
	}
}

func TestStreamOutput_tailMoreThanContent(t *testing.T) {
	// Asking for more lines than the file has yields the whole file.
	path := writeTempFile(t, "only\ntwo\n")
	var buf bytes.Buffer
	if err := StreamOutput(context.Background(), &buf, path, false, 10); err != nil {
		t.Fatalf("StreamOutput: %v", err)
	}
	if buf.String() != "only\ntwo\n" {
		t.Errorf("output = %q, want whole file", buf.String())
	}
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of the follow
// loop (writer goroutine) and the test's reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestStreamOutput_followCancels(t *testing.T) {
	path := writeTempFile(t, "start\n")
	ctx, cancel := context.WithCancel(context.Background())

	var buf syncBuffer
	done := make(chan error, 1)
	go func() { done <- StreamOutput(ctx, &buf, path, true, 0) }()

	// Append after the initial copy so the follow loop picks it up.
	if err := appendToFile(t, path, "more\n"); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Wait until the appended bytes have been streamed, then cancel.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "more") {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("appended bytes never streamed; got %q", buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("StreamOutput returned error on cancel: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "start") || !strings.Contains(out, "more") {
		t.Errorf("output = %q, want to contain start and more", out)
	}
}

func TestStreamOutput_missingFile(t *testing.T) {
	err := StreamOutput(context.Background(), &bytes.Buffer{}, filepath.Join(t.TempDir(), "nope.log"), false, 0)
	if err == nil || !strings.Contains(err.Error(), "opening agent output log") {
		t.Fatalf("expected open error, got %v", err)
	}
}

func TestLatestOutputPath_none(t *testing.T) {
	tmp := t.TempDir()
	agentLogsDirOverride = tmp
	t.Cleanup(func() { agentLogsDirOverride = "" })

	_, err := LatestOutputPath("ghost")
	if err == nil || !strings.Contains(err.Error(), "no executions for agent") {
		t.Fatalf("expected no-executions error, got %v", err)
	}
}

func TestLatestOutputPath_found(t *testing.T) {
	tmp := t.TempDir()
	agentLogsDirOverride = tmp
	t.Cleanup(func() { agentLogsDirOverride = "" })

	exe, err := NewExecution(LaunchRecord{ID: "abc123", Agent: "seen", StartedAt: time.Now()})
	if err != nil {
		t.Fatalf("NewExecution: %v", err)
	}
	path, err := LatestOutputPath("seen")
	if err != nil {
		t.Fatalf("LatestOutputPath: %v", err)
	}
	if want := filepath.Join(exe.Dir(), "output.log"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func appendToFile(t *testing.T, path, s string) error {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(s)
	return err
}
