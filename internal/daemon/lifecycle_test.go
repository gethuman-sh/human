package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLifecycleRunner records every Run call and returns queued output/error.
type fakeLifecycleRunner struct {
	calls [][]string
	err   error
}

func (f *fakeLifecycleRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return nil, f.err
}

func TestResolveCLIPath_found(t *testing.T) {
	path, err := ResolveCLIPath(func(file string) (string, error) {
		assert.Equal(t, "human", file)
		return "/usr/local/bin/human", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "/usr/local/bin/human", path)
}

func TestResolveCLIPath_notFound(t *testing.T) {
	_, err := ResolveCLIPath(func(string) (string, error) {
		return "", exec.ErrNotFound
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "human CLI not found")
}

func TestStopIfRunning_NoPidFile_NoOp(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	runner := &fakeLifecycleRunner{}
	err := StopIfRunning(runner, "/usr/local/bin/human")

	require.NoError(t, err)
	assert.Empty(t, runner.calls)
}

func TestStopIfRunning_Alive_CallsDaemonStop(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	require.NoError(t, WritePidFile(os.Getpid()))
	defer RemovePidFile()

	runner := &fakeLifecycleRunner{}
	err := StopIfRunning(runner, "/usr/local/bin/human")

	require.NoError(t, err)
	require.Len(t, runner.calls, 1)
	assert.Equal(t, []string{"/usr/local/bin/human", "daemon", "stop"}, runner.calls[0])
}

func TestStopIfRunning_RunnerError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	require.NoError(t, WritePidFile(os.Getpid()))
	defer RemovePidFile()

	runner := &fakeLifecycleRunner{err: errors.New("boom")}
	err := StopIfRunning(runner, "/usr/local/bin/human")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopping daemon")
}

func TestStartForProject_RunnerError(t *testing.T) {
	runner := &fakeLifecycleRunner{err: errors.New("boom")}
	err := StartForProject(runner, "/usr/local/bin/human", "/some/dir", time.Second)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "starting daemon")
	// No polling should occur when the start subprocess itself fails.
	require.Len(t, runner.calls, 1)
}

func TestStartForProject_TimesOut(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	runner := &fakeLifecycleRunner{}
	err := StartForProject(runner, "/usr/local/bin/human", "/some/dir", 150*time.Millisecond)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon did not become reachable")
}

func TestStartForProject_BecomesReachable(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	require.NoError(t, WriteInfo(DaemonInfo{Addr: ln.Addr().String()}))
	defer RemoveInfo()

	runner := &fakeLifecycleRunner{}
	err = StartForProject(runner, "/usr/local/bin/human", "/some/dir", 2*time.Second)

	require.NoError(t, err)
}
