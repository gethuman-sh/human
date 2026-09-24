package cmdagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
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

func TestBuildAgentCmd_hasSendSubcommand(t *testing.T) {
	cmd := BuildAgentCmd()
	for _, sub := range cmd.Commands() {
		if sub.Name() == "send" {
			return
		}
	}
	t.Error("missing send subcommand")
}

func TestBuildSendCmd_argsValidation(t *testing.T) {
	cmd := BuildAgentCmd()
	cmd.SetArgs([]string{"send", "only-one-arg"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when message arg is missing, got nil")
	}
}

func TestBuildLogsCmd_followFlagRegistered(t *testing.T) {
	cmd := buildLogsCmd()
	follow := cmd.Flags().Lookup("follow")
	if follow == nil {
		t.Fatal("missing --follow flag")
	}
	if follow.Shorthand != "f" {
		t.Errorf("follow shorthand = %q, want %q", follow.Shorthand, "f")
	}
	if cmd.Flags().Lookup("tail") == nil {
		t.Error("missing --tail flag")
	}
}

// A daemon too old to take the async signal must not abort the stop: the
// synchronous path below needs no daemon at all (SC-5397).
func TestAsyncStopClient_protocolStaleDaemonFallsThrough(t *testing.T) {
	if daemon.MinDaemonProtocol <= 1 {
		t.Skip("no rejectable protocol below MinDaemonProtocol")
	}
	client, err := asyncStopClient(daemon.DaemonInfo{Addr: "127.0.0.1:19285", Protocol: daemon.MinDaemonProtocol - 1})
	require.NoError(t, err)
	assert.Nil(t, client, "a stale daemon means fall through, not fail")
}

func TestAsyncStopClient_currentDaemonReturnsClient(t *testing.T) {
	client, err := asyncStopClient(daemon.DaemonInfo{Addr: "127.0.0.1:19285", Protocol: daemon.MinDaemonProtocol})
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "127.0.0.1:19285", client.Info().Addr)
}

// Which container model runs is a property of the project, not of the command
// that started it — but an explicit --model still wins, because a person naming
// a tier on the command line is making the decision the setting only defaults
// (SC-5474).
func TestResolveStartModel(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte("agent:\n  model: sonnet\n"), 0o600))

	assert.Equal(t, "sonnet", resolveStartModel("", dir))
	assert.Equal(t, "opus", resolveStartModel("opus", dir), "the flag wins")
	assert.Empty(t, resolveStartModel("", t.TempDir()), "no setting leaves the account default in force")
}
