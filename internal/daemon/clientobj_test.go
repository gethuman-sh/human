package daemon

import (
	"net"
	"testing"

	"github.com/gethuman-sh/human/errors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listenAt returns a closed-on-cleanup listener's address, so IsReachable finds
// something to connect to.
func listenAt(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

func TestNewClient_CarriesEndpointAndVersion(t *testing.T) {
	c, err := NewClient(DaemonInfo{Addr: "1.2.3.4:19285", Token: "tok", ChromeAddr: "1.2.3.4:19286"})
	require.NoError(t, err)
	assert.Equal(t, "1.2.3.4:19285", c.Info().Addr)
	assert.Equal(t, "1.2.3.4:19286", c.Info().ChromeAddr)
	assert.Equal(t, ClientVersion, c.version)
}

// The gate exists so no path reaches the daemon ungated; before Client there
// was one caller of it in main.go and everything else went around.
func TestNewClient_RefusesTooOldDaemon(t *testing.T) {
	if MinDaemonProtocol <= 1 {
		// Protocol 0 is the transition carve-out, so at floor 1 there is no
		// value left to reject. The wiring is covered below regardless.
		t.Skip("no rejectable protocol below MinDaemonProtocol")
	}
	_, err := NewClient(DaemonInfo{Addr: "1.2.3.4:19285", Protocol: MinDaemonProtocol - 1})
	require.Error(t, err)
	assert.True(t, IsProtocolError(err))
}

// The refusal must stay recognisable through the detail wrapping, or main falls
// back to running the command locally against no daemon.
func TestIsProtocolError_SurvivesWrapping(t *testing.T) {
	err := errors.WrapWithDetails(errProtocolTooOld, "daemon speaks protocol %d", "daemon_protocol", 0)
	assert.True(t, IsProtocolError(err))
	assert.False(t, IsProtocolError(errors.WithDetails("something else")))
	assert.False(t, IsProtocolError(nil))
}

func TestNewClient_AcceptsDaemonThatAdvertisesNoProtocol(t *testing.T) {
	c, err := NewClient(DaemonInfo{Addr: "1.2.3.4:19285"})
	require.NoError(t, err)
	assert.NotNil(t, c)
}

func TestIsProtocolError_DistinguishesUnreachable(t *testing.T) {
	withMemFs(t)
	t.Setenv("HUMAN_DAEMON_ADDR", "")
	t.Setenv("HUMAN_DAEMON_TOKEN", "")

	_, err := Connect()
	require.Error(t, err)
	// "no daemon" and "daemon too old" need opposite responses from main: run
	// locally versus stop and report.
	assert.False(t, IsProtocolError(err))
}

func TestResolveInfo_EnvAddrWins(t *testing.T) {
	withMemFs(t)
	require.NoError(t, WriteInfo(DaemonInfo{Addr: "127.0.0.1:1", Token: "filetok", Version: "9.9.9"}))
	t.Setenv("HUMAN_DAEMON_ADDR", "10.0.0.1:19285")
	t.Setenv("HUMAN_DAEMON_TOKEN", "")

	info, err := resolveInfo()
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1:19285", info.Addr)
	// The token and the rest of the file come along: the skew warning and the
	// protocol gate read them and they describe the same daemon.
	assert.Equal(t, "filetok", info.Token)
	assert.Equal(t, "9.9.9", info.Version)
}

func TestResolveInfo_EnvTokenOverridesFile(t *testing.T) {
	withMemFs(t)
	require.NoError(t, WriteInfo(DaemonInfo{Addr: "127.0.0.1:1", Token: "filetok"}))
	t.Setenv("HUMAN_DAEMON_ADDR", "10.0.0.1:19285")
	t.Setenv("HUMAN_DAEMON_TOKEN", "envtok")

	info, err := resolveInfo()
	require.NoError(t, err)
	assert.Equal(t, "envtok", info.Token)
}

func TestResolveInfo_ReachableFileWinsOverFallback(t *testing.T) {
	withMemFs(t)
	addr := listenAt(t)
	require.NoError(t, WriteInfo(DaemonInfo{Addr: addr, Token: "filetok", ProxyAddr: "127.0.0.1:19287"}))
	t.Setenv("HUMAN_DAEMON_ADDR", "")
	t.Setenv("HUMAN_DAEMON_TOKEN", "")

	info, err := resolveInfo()
	require.NoError(t, err)
	assert.Equal(t, addr, info.Addr)
	assert.Equal(t, "127.0.0.1:19287", info.ProxyAddr)
}

// The container fallback must synthesize all three addresses. Returning only
// Addr is what leaves an agent container with no HUMAN_PROXY_ADDR.
func TestResolveInfo_DockerFallbackCarriesAllThreeAddresses(t *testing.T) {
	withMemFs(t)
	t.Setenv("HUMAN_DAEMON_ADDR", "")
	t.Setenv("HUMAN_DAEMON_TOKEN", "")

	fallback := DaemonInfo{
		Addr:       DockerHost + ":19285",
		ChromeAddr: DockerHost + ":19286",
		ProxyAddr:  DockerHost + ":19287",
	}
	if !fallback.IsReachable() {
		t.Skip("host.docker.internal is not reachable from this machine")
	}

	info, err := resolveInfo()
	require.NoError(t, err)
	assert.Equal(t, fallback.Addr, info.Addr)
	assert.Equal(t, fallback.ChromeAddr, info.ChromeAddr)
	assert.Equal(t, fallback.ProxyAddr, info.ProxyAddr)
}

func TestResolveInfo_NoDaemonReports(t *testing.T) {
	withMemFs(t)
	t.Setenv("HUMAN_DAEMON_ADDR", "")
	t.Setenv("HUMAN_DAEMON_TOKEN", "")

	_, err := resolveInfo()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "human daemon not reachable")
}
