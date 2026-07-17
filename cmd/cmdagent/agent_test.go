package cmdagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildAgentCmd_hasSubcommands(t *testing.T) {
	cmd := BuildAgentCmd()

	want := []string{"start", "stop", "list", "attach", "logs"}
	subs := cmd.Commands()

	found := make(map[string]bool)
	for _, sub := range subs {
		found[sub.Name()] = true
	}

	for _, w := range want {
		if !found[w] {
			t.Errorf("missing subcommand %q", w)
		}
	}
}

func TestBuildAgentCmd_startRequiresName(t *testing.T) {
	cmd := BuildAgentCmd()
	cmd.SetArgs([]string{"start"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when name is missing, got nil")
	}
	if !strings.Contains(err.Error(), "accepts 1 arg") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildAgentCmd_stopRequiresName(t *testing.T) {
	cmd := BuildAgentCmd()
	cmd.SetArgs([]string{"stop"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when name is missing, got nil")
	}
}

func TestBuildAgentCmd_listNoArgs(t *testing.T) {
	cmd := BuildAgentCmd()
	cmd.SetArgs([]string{"list", "extra"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when list gets extra args, got nil")
	}
}

func TestAgentLogsCmd_NoLogs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var out bytes.Buffer
	cmd := BuildAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"logs", "missing"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("logs on unknown agent must not error: %v", err)
	}
	if !strings.Contains(out.String(), "no execution logs") {
		t.Errorf("expected no-logs message, got %q", out.String())
	}
}

func TestAgentLogsCmd_RendersRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed one finished run on disk exactly as the store lays it out.
	dir := filepath.Join(home, ".human", "agent-logs", "a1", "abcdef1234567890")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	launch := map[string]any{
		"id": "abcdef1234567890", "agent": "a1", "prompt": "p",
		"argv": []string{"claude", "-p", "p"}, "model": "opus",
		"container_id": "cid", "started_at": time.Now().Format(time.RFC3339Nano),
	}
	writeTestJSON(t, filepath.Join(dir, "launch.json"), launch)
	outcome := map[string]any{
		"reason": "completed", "exit_code": 0, "duration_ms": 65000,
		"ended_at": time.Now().Format(time.RFC3339Nano),
	}
	writeTestJSON(t, filepath.Join(dir, "outcome.json"), outcome)

	var out bytes.Buffer
	cmd := BuildAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"logs", "a1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("logs: %v", err)
	}
	for _, want := range []string{"abcdef123456", "opus", "completed", dir} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table missing %q in output:\n%s", want, out.String())
		}
	}
}

func writeTestJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
