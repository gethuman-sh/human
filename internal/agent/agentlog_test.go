package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/devcontainer"
	"github.com/gethuman-sh/human/internal/gitrepo"
)

// stderrFrame wraps payload in a Docker stdcopy multiplexed stderr frame.
func stderrFrame(payload string) []byte {
	h := make([]byte, 8)
	h[0] = 2 // stderr stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, []byte(payload)...)
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
func nopCloser() io.Closer           { return io.NopCloser(strings.NewReader("")) }
func contains(s, sub string) bool    { return strings.Contains(s, sub) }

func withLogRoot(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	agentLogsDirOverride = tmp
	t.Cleanup(func() { agentLogsDirOverride = "" })
	return tmp
}

func TestExecClaudeDetached_WritesLaunchRecord(t *testing.T) {
	withLogRoot(t)
	mgr := &Manager{Docker: &mockDockerClient{}}
	exe, err := mgr.execClaudeDetached(context.Background(), "cid", "vscode", "", StartOpts{
		Name: "a", Prompt: "P", Model: "opus",
	})
	if err != nil {
		t.Fatalf("execClaudeDetached: %v", err)
	}
	if exe == nil {
		t.Fatal("expected non-nil execution")
	}
	if !containsArg(exe.Launch.Argv, "-p") || !containsArg(exe.Launch.Argv, "P") {
		t.Fatalf("argv missing -p/P: %v", exe.Launch.Argv)
	}
	if !containsArg(exe.Launch.Argv, "--model") || !containsArg(exe.Launch.Argv, "opus") {
		t.Fatalf("argv missing --model/opus: %v", exe.Launch.Argv)
	}
	if exe.Launch.ContainerID != "cid" {
		t.Fatalf("container_id = %q, want cid", exe.Launch.ContainerID)
	}
}

// teeMock emits a stdout and a stderr frame so the tee demux can be observed.
type teeMock struct {
	mockDockerClient
}

func (m *teeMock) ExecAttach(_ context.Context, _ string) (devcontainer.ExecAttachResponse, error) {
	frames := append(stdoutFrame("OUT"), stderrFrame("ERR")...)
	return devcontainer.ExecAttachResponse{
		Reader: bytesReader(frames),
		Conn:   nopCloser(),
	}, nil
}

func TestTeeExecOutput_DemuxesStdoutAndStderr(t *testing.T) {
	withLogRoot(t)
	mgr := &Manager{Docker: &teeMock{}}
	exe, err := mgr.execClaudeDetached(context.Background(), "cid", "vscode", "", StartOpts{Name: "tee", Prompt: "p"})
	if err != nil {
		t.Fatalf("execClaudeDetached: %v", err)
	}
	if exe == nil {
		t.Fatal("expected non-nil execution")
	}
	// Poll for the tee goroutine to flush both frames.
	var out string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(filepath.Join(exe.Dir(), "output.log"))
		out = string(data)
		if len(out) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !contains(out, "OUT") || !contains(out, "ERR") {
		t.Fatalf("output.log missing demuxed payloads, got %q", out)
	}
}

func TestListExecutions_NewestFirst(t *testing.T) {
	withLogRoot(t)
	old, err := NewExecution(LaunchRecord{ID: "old", Agent: "a", StartedAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.RecordOutcome(OutcomeRecord{Reason: "completed", EndedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecution(LaunchRecord{ID: "new", Agent: "a", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	execs, err := ListExecutions("a")
	if err != nil {
		t.Fatal(err)
	}
	if len(execs) != 2 {
		t.Fatalf("want 2 executions, got %d", len(execs))
	}
	if execs[0].Launch.ID != "new" {
		t.Fatalf("newest-first violated: %q", execs[0].Launch.ID)
	}
	if execs[0].Outcome != nil {
		t.Fatal("newest run has no outcome yet; want nil")
	}
	if execs[1].Outcome == nil || execs[1].Outcome.Reason != "completed" {
		t.Fatalf("old run outcome not attached: %+v", execs[1].Outcome)
	}
}

func TestListExecutions_MissingAgentIsEmpty(t *testing.T) {
	withLogRoot(t)
	execs, err := ListExecutions("nope")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(execs) != 0 {
		t.Fatalf("want empty, got %d", len(execs))
	}
}

func TestPruneExecutions_DropsOld(t *testing.T) {
	withLogRoot(t)
	if _, err := NewExecution(LaunchRecord{ID: "stale", Agent: "a", StartedAt: time.Now().Add(-100 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecution(LaunchRecord{ID: "fresh", Agent: "a", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	removed, err := PruneExecutions()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("want 1 pruned, got %d", removed)
	}
	execs, _ := ListExecutions("a")
	if len(execs) != 1 || execs[0].Launch.ID != "fresh" {
		t.Fatalf("expected only fresh to remain: %+v", execs)
	}
}

func TestPruneExecutions_RemovesKeptWorktree(t *testing.T) {
	withLogRoot(t)
	wt := filepath.Join(t.TempDir(), "kept-wt")
	if err := os.MkdirAll(wt, 0o700); err != nil {
		t.Fatal(err)
	}
	var removed []string
	prev := gitrepo.WorktreeRemove
	gitrepo.WorktreeRemove = func(_ context.Context, _, path string) error {
		removed = append(removed, path)
		return nil
	}
	t.Cleanup(func() { gitrepo.WorktreeRemove = prev })

	if _, err := NewExecution(LaunchRecord{
		ID: "stale", Agent: "a", StartedAt: time.Now().Add(-100 * 24 * time.Hour), Worktree: wt,
	}); err != nil {
		t.Fatal(err)
	}

	n, err := PruneExecutions()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 pruned, got %d", n)
	}
	if len(removed) != 1 || removed[0] != wt {
		t.Fatalf("WorktreeRemove = %v, want [%s]", removed, wt)
	}
	if _, statErr := os.Stat(wt); !os.IsNotExist(statErr) {
		t.Fatalf("kept worktree dir should be gone, stat err = %v", statErr)
	}
}

func TestPruneExecutions_KeepsRecentWorktree(t *testing.T) {
	withLogRoot(t)
	var removed []string
	prev := gitrepo.WorktreeRemove
	gitrepo.WorktreeRemove = func(_ context.Context, _, path string) error {
		removed = append(removed, path)
		return nil
	}
	t.Cleanup(func() { gitrepo.WorktreeRemove = prev })

	if _, err := NewExecution(LaunchRecord{
		ID: "fresh", Agent: "a", StartedAt: time.Now(), Worktree: "/wt/fresh",
	}); err != nil {
		t.Fatal(err)
	}

	n, err := PruneExecutions()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want 0 pruned, got %d", n)
	}
	if len(removed) != 0 {
		t.Fatalf("WorktreeRemove = %v, want none for a recent run", removed)
	}
}

func TestStopReason(t *testing.T) {
	if stopReason(Meta{Status: StatusFailed}) != "reaped" {
		t.Fatal("failed status should map to reaped")
	}
	if stopReason(Meta{Status: StatusRunning}) != "completed" {
		t.Fatal("running/stop should map to completed")
	}
}

func TestLookupExecution_PrefersIDThenLatest(t *testing.T) {
	withLogRoot(t)
	if _, err := NewExecution(LaunchRecord{ID: "id1", Agent: "a", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	exe := lookupExecution(Meta{Name: "a", ExecutionID: "id1"})
	if exe == nil || exe.Launch.ID != "id1" {
		t.Fatalf("lookup by id failed: %+v", exe)
	}
	// No id -> falls back to latest.
	exe = lookupExecution(Meta{Name: "a"})
	if exe == nil {
		t.Fatal("expected latest fallback")
	}
	// Unknown agent -> nil.
	if lookupExecution(Meta{Name: "missing"}) != nil {
		t.Fatal("unknown agent should yield nil")
	}
}

func TestHookEventSink_AppendsJSONL(t *testing.T) {
	withLogRoot(t)
	if _, err := NewExecution(LaunchRecord{ID: "run1", Agent: "a", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	HookEventSink(hookevents.Event{EventName: "Stop", AgentName: "a", Timestamp: time.Now()})
	HookEventSink(hookevents.Event{EventName: "Notification", AgentName: "a", Timestamp: time.Now()})

	exe, err := LatestExecution("a")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(exe.Dir(), "hooks.jsonl"))
	if err != nil {
		t.Fatalf("hooks.jsonl not written: %v", err)
	}
	lines := strings.Count(string(data), "\n")
	if lines != 2 {
		t.Fatalf("want 2 json lines, got %d: %q", lines, string(data))
	}

	// Empty agent name is a no-op (no panic, nothing written).
	HookEventSink(hookevents.Event{EventName: "Stop", Timestamp: time.Now()})
}
