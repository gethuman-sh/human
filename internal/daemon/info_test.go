package daemon

import (
	"encoding/json"
	"net"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteInfo_RoundTrips verifies the bytes WriteInfo lays down on the core
// (swappable) filesystem unmarshal back to the same DaemonInfo. ReadInfo itself
// now lives in the human-daemon-client contract and reads the real OS fs, so it
// is covered by that module's own TestReadInfo_success rather than here.
func TestWriteInfo_RoundTrips(t *testing.T) {
	withMemFs(t)

	info := DaemonInfo{
		Addr:       "192.168.1.5:19285",
		ChromeAddr: "192.168.1.5:19286",
		ProxyAddr:  "192.168.1.5:19287",
		Token:      "abc123",
		PID:        12345,
	}

	require.NoError(t, WriteInfo(info))

	data, err := afero.ReadFile(fs, InfoPath())
	require.NoError(t, err)
	var got DaemonInfo
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, info, got)
}

func TestWriteInfo_CreatesDirectory(t *testing.T) {
	withMemFs(t)

	info := DaemonInfo{Addr: "localhost:19285", Token: "tok", PID: 1}
	err := WriteInfo(info)
	require.NoError(t, err)

	exists, err := afero.DirExists(fs, InfoPath()[:len(InfoPath())-len("/daemon.json")])
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestWriteInfo_RestrictedPermissions(t *testing.T) {
	withMemFs(t)

	info := DaemonInfo{Addr: "localhost:19285", Token: "secret", PID: 1}
	require.NoError(t, WriteInfo(info))

	fi, err := fs.Stat(InfoPath())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

func TestRemoveInfo(t *testing.T) {
	withMemFs(t)

	info := DaemonInfo{Addr: "localhost:19285", Token: "tok", PID: 1}
	require.NoError(t, WriteInfo(info))

	RemoveInfo()

	exists, err := afero.Exists(fs, InfoPath())
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestRemoveInfo_NoFile(t *testing.T) {
	withMemFs(t)
	// Should not panic or error when file doesn't exist.
	RemoveInfo()
}

func TestDaemonInfo_IsAlive_CurrentProcess(t *testing.T) {
	info := DaemonInfo{PID: os.Getpid()}
	assert.True(t, info.IsAlive())
}

func TestDaemonInfo_IsAlive_InvalidPID(t *testing.T) {
	info := DaemonInfo{PID: -1}
	assert.False(t, info.IsAlive())
}

func TestDaemonInfo_IsAlive_ZeroPID(t *testing.T) {
	info := DaemonInfo{PID: 0}
	assert.False(t, info.IsAlive())
}

func TestDaemonInfo_IsReachable_ListeningServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	info := DaemonInfo{Addr: ln.Addr().String()}
	assert.True(t, info.IsReachable())
}

func TestDaemonInfo_IsReachable_NoServer(t *testing.T) {
	info := DaemonInfo{Addr: "127.0.0.1:1"}
	assert.False(t, info.IsReachable())
}

func TestDaemonInfo_IsReachable_EmptyAddr(t *testing.T) {
	info := DaemonInfo{}
	assert.False(t, info.IsReachable())
}
