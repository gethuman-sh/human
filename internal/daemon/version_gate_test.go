package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientVersionSupported(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"dev", true},
		{"dev (abc123) 2026-07-13", true},
		{MinClientVersion, true},
		{"v" + MinClientVersion, true},
		{"0.21.0-rc1", true},
		{"0.21.1", true},
		{"0.22.0", true},
		{"1.0.0", true},
		{"99.0", true},
		// Pre-handshake and pre-grant-protocol clients.
		{"", false},
		{"0.20.0", false},
		{"0.20.9", false},
		{"0.9.9", false},
		{"garbage", false},
		{"v.x", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, clientVersionSupported(tt.version), "version %q", tt.version)
	}
}

func TestServer_VersionGate_RejectsStaleClientBeforeSideEffects(t *testing.T) {
	token := "test-token"
	addr, _, store := startTestServerWithConfirm(t, token)

	// A destructive op from a pre-grant-protocol client: rejected with a
	// clear upgrade message and — critically — NOTHING queued.
	resp := sendRequest(t, addr, Request{Version: "0.20.0", Token: token, ClientPID: 1111, Args: []string{"jira", "issue", "delete", "KAN-1"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.False(t, resp.AwaitConfirm)
	assert.Contains(t, resp.Stderr, "older than this daemon supports")
	assert.Contains(t, resp.Stderr, MinClientVersion)
	assert.Equal(t, 0, store.Len(), "stale client must not create queue entries")
}

func TestServer_VersionGate_DevAndCurrentPass(t *testing.T) {
	token := "test-token"
	addr, _, _ := startTestServerWithConfirm(t, token)

	// Non-destructive request with dev version reaches routing (echoCmd
	// runs and echoes the args back).
	devResp := sendRequest(t, addr, Request{Version: "dev", Token: token, Args: []string{"echo", "hello"}})
	require.Equal(t, 0, devResp.ExitCode, "stderr: %s", devResp.Stderr)

	relResp := sendRequest(t, addr, Request{Version: MinClientVersion, Token: token, Args: []string{"echo", "hello"}})
	assert.Equal(t, 0, relResp.ExitCode, "stderr: %s", relResp.Stderr)
}

func TestServer_VersionGate_RunsAfterAuth(t *testing.T) {
	token := "test-token"
	addr, _, _ := startTestServerWithConfirm(t, token)

	// Wrong token + stale version: authentication wins, so the gate leaks
	// no protocol details to unauthenticated callers.
	resp := sendRequest(t, addr, Request{Version: "0.20.0", Token: "wrong", Args: []string{"echo", "hello"}})
	assert.Contains(t, resp.Stderr, "authentication failed")
}

func TestClientSupported_ProtocolGate(t *testing.T) {
	// A client advertising a protocol gets the integer gate: at or above
	// MinProtocol passes regardless of its version string; below is rejected.
	assert.True(t, clientSupported("0.19.0", MinProtocol))
	assert.True(t, clientSupported("", MinProtocol+5), "newer clients pass; their own MinDaemonProtocol guards the other direction")
	if MinProtocol > 1 {
		assert.False(t, clientSupported("99.0.0", MinProtocol-1), "a too-old protocol is rejected even with a high version string")
	}

	// Protocol zero = pre-handshake client → legacy version-string gate.
	assert.True(t, clientSupported("dev", 0))
	assert.True(t, clientSupported(MinClientVersion, 0))
	assert.False(t, clientSupported("0.20.0", 0))
}

func TestServer_ProtocolGate_AcceptsAndRejects(t *testing.T) {
	token := "test-token"
	addr, _, store := startTestServerWithConfirm(t, token)

	ok := sendRequest(t, addr, Request{Version: "0.1.0", Protocol: MinProtocol, Token: token, Args: []string{"echo", "hello"}})
	assert.Equal(t, 0, ok.ExitCode, "stderr: %s", ok.Stderr)

	if MinProtocol > 1 {
		tooOld := sendRequest(t, addr, Request{Version: "99.0.0", Protocol: MinProtocol - 1, Token: token, ClientPID: 1111, Args: []string{"jira", "issue", "delete", "KAN-1"}})
		assert.Equal(t, 1, tooOld.ExitCode)
		assert.Contains(t, tooOld.Stderr, "wire protocol")
		assert.Equal(t, 0, store.Len(), "protocol-stale client must not create queue entries")
	}
}

func TestDaemonProtocolError(t *testing.T) {
	// Daemons predating protocol advertising pass — refusing them would
	// strand every client during the transition.
	assert.NoError(t, DaemonProtocolError(DaemonInfo{Protocol: 0}))
	assert.NoError(t, DaemonProtocolError(DaemonInfo{Protocol: MinDaemonProtocol}))
	assert.NoError(t, DaemonProtocolError(DaemonInfo{Protocol: MinDaemonProtocol + 3}))

	err := DaemonProtocolError(DaemonInfo{Protocol: MinDaemonProtocol - 1})
	if MinDaemonProtocol > 1 {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rebuild and restart the daemon")
	} else {
		// MinDaemonProtocol 1 means "protocol 0" is the only lower value, and
		// zero is the transition carve-out above — nothing to reject yet.
		assert.NoError(t, err)
	}
}

func TestProtocolConstants_Sane(t *testing.T) {
	// The compatibility floors can never exceed what this build speaks.
	assert.LessOrEqual(t, MinProtocol, Protocol)
	assert.LessOrEqual(t, MinDaemonProtocol, Protocol)
	assert.GreaterOrEqual(t, MinProtocol, 1)
}
