package cmddaemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDaemonStartCmd_InteractiveRequiresForeground(t *testing.T) {
	t.Setenv(daemonChildEnv, "")

	cmd := buildDaemonStartCmd(nil, "")
	cmd.SetArgs([]string{"--interactive"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--interactive requires --foreground")
}

func TestDaemonStartCmd_ForegroundFlag(t *testing.T) {
	cmd := buildDaemonStartCmd(nil, "")
	fg := cmd.Flags().Lookup("foreground")
	require.NotNil(t, fg, "expected --foreground flag to exist")
	assert.Equal(t, "false", fg.DefValue)
}

func TestDaemonLogPath(t *testing.T) {
	p := DaemonLogPath()
	assert.Contains(t, p, "daemon.log")
	assert.Contains(t, p, ".human")
}

func TestDaemonPidPath(t *testing.T) {
	p := DaemonPidPath()
	assert.Contains(t, p, "daemon.pid")
	assert.Contains(t, p, ".human")
}

func TestWriteAndReadPidFile(t *testing.T) {
	// Use a temp dir to avoid polluting the real ~/.human.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	pid := os.Getpid()
	err := WritePidFile(pid)
	require.NoError(t, err)

	// Verify the file exists with correct content.
	data, err := os.ReadFile(filepath.Join(tmpDir, ".human", "daemon.pid"))
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(pid), string(data))

	// ReadAlivePid should find our own process alive.
	gotPid, alive := ReadAlivePid()
	assert.Equal(t, pid, gotPid)
	assert.True(t, alive)

	// Clean up.
	RemovePidFile()
	_, err = os.Stat(filepath.Join(tmpDir, ".human", "daemon.pid"))
	assert.True(t, os.IsNotExist(err))
}

func TestReadAlivePid_NoPidFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	pid, alive := ReadAlivePid()
	assert.Equal(t, 0, pid)
	assert.False(t, alive)
}

func TestReadAlivePid_DeadProcess(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Write a PID that almost certainly doesn't exist.
	err := WritePidFile(999999999)
	require.NoError(t, err)

	pid, alive := ReadAlivePid()
	assert.Equal(t, 999999999, pid)
	assert.False(t, alive)
}

func TestBuildDaemonStopCmd_Exists(t *testing.T) {
	cmd := buildDaemonStopCmd()
	assert.Equal(t, "stop", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestBuildDaemonStopCmd_NoPidFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	cmd := buildDaemonStopCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "not running")
}

func TestBuildDaemonStatusCmd_PidInfo(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// No PID file, unreachable addr -> "not running".
	cmd := buildDaemonStatusCmd()
	cmd.SetArgs([]string{"--addr", "localhost:19999"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, buf.String(), "not running")
}

func TestBuildDaemonStatusCmd_WithPidNotReachable(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Write our own PID so the file exists and process is "alive".
	err := WritePidFile(os.Getpid())
	require.NoError(t, err)

	cmd := buildDaemonStatusCmd()
	cmd.SetArgs([]string{"--addr", "localhost:19999"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, buf.String(), "running")
	assert.Contains(t, buf.String(), "not reachable")
}

func TestDaemonCmd_StopRegistered(t *testing.T) {
	cmd := BuildDaemonCmd(nil, "")
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "stop" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected stop subcommand to be registered")
}

func TestSwapLoopbackHost(t *testing.T) {
	tests := []struct {
		name  string
		addr  string
		reach string
		want  string
	}{
		{"loopback swapped to bridge", "127.0.0.1:19285", "172.17.0.1", "172.17.0.1:19285"},
		{"localhost swapped", "localhost:19285", "172.17.0.1", "172.17.0.1:19285"},
		{"empty host swapped", ":19285", "172.17.0.1", "172.17.0.1:19285"},
		{"loopback stays loopback on desktop", "127.0.0.1:19285", "127.0.0.1", "127.0.0.1:19285"},
		{"explicit non-loopback respected", "192.168.1.5:19285", "172.17.0.1", "192.168.1.5:19285"},
		{"wildcard respected (operator override)", "0.0.0.0:19285", "172.17.0.1", "0.0.0.0:19285"},
		{"malformed addr returned as-is", "not-an-addr", "172.17.0.1", "not-an-addr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, swapLoopbackHost(tt.addr, tt.reach))
		})
	}
}
