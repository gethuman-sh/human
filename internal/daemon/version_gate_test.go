package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// ledgerRow is one line of the docs/protocol.md table, parsed by COLUMN NAME
// rather than by offset from the end, so adding a column to the ledger fails
// here loudly instead of silently comparing the wrong number.
type ledgerRow struct {
	protocol, minProtocol, minDaemonProtocol int
}

// parseProtocolLedger reads the ledger out of docs/protocol.md. It is the
// document the rule lives in, so it is the document the test reads — deriving
// the expectation from anything else would make the test agree with itself
// while the rule stayed unenforced.
func parseProtocolLedger(t *testing.T) []ledgerRow {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "docs", "protocol.md"))
	require.NoError(t, err)
	data, err := os.ReadFile(path) //nolint:gosec // fixed repo-relative path
	require.NoError(t, err, "docs/protocol.md is where the ledger rule is written")

	var rows []ledgerRow
	for _, line := range strings.Split(string(data), "\n") {
		cells := strings.Split(strings.TrimSpace(line), "|")
		if len(cells) != 7 {
			continue // not a ledger row: prose, the header, or the separator
		}
		proto, err := strconv.Atoi(strings.TrimSpace(cells[1]))
		if err != nil {
			continue // the header row, whose first cell is "Protocol"
		}
		minProto, err := strconv.Atoi(strings.TrimSpace(cells[4]))
		require.NoError(t, err, "MinProtocol column of ledger row %d", proto)
		minDaemon, err := strconv.Atoi(strings.TrimSpace(cells[5]))
		require.NoError(t, err, "MinDaemonProtocol column of ledger row %d", proto)
		rows = append(rows, ledgerRow{proto, minProto, minDaemon})
	}
	return rows
}

// TestProtocolLedger_CoversThisBuild is the rule in docs/protocol.md with
// something behind it. Until now the only thing enforcing "bump Protocol and
// add a ledger line" was a reviewer remembering to look, and a wire change
// shipped past it twice (SC-4820 changed response shapes and bumped nothing;
// SC-4923 added a route and was caught only in review). A rule nobody enforces
// is a rule that gets deleted, so this test is the alternative to deleting it.
func TestProtocolLedger_CoversThisBuild(t *testing.T) {
	rows := parseProtocolLedger(t)
	require.NotEmpty(t, rows, "no ledger rows parsed — has the table format changed?")

	last := rows[len(rows)-1]
	assert.Equal(t, Protocol, last.protocol,
		"this build speaks protocol %d but docs/protocol.md's ledger ends at %d — every wire change bumps Protocol AND adds a ledger line",
		Protocol, last.protocol)
	assert.Equal(t, MinProtocol, last.minProtocol,
		"ledger row %d records MinProtocol %d, the code says %d", last.protocol, last.minProtocol, MinProtocol)
	assert.Equal(t, MinDaemonProtocol, last.minDaemonProtocol,
		"ledger row %d records MinDaemonProtocol %d, the code says %d", last.protocol, last.minDaemonProtocol, MinDaemonProtocol)

	// "Never reuse or renumber. The ledger is append-only." — one row per
	// protocol, in order, no gaps: a gap means a bump whose reasoning was
	// never written down.
	for i, row := range rows {
		assert.Equal(t, i+1, row.protocol, "ledger row %d is out of order or a number was reused/skipped", i+1)
		assert.LessOrEqual(t, row.minProtocol, row.protocol, "ledger row %d floors MinProtocol above the protocol itself", row.protocol)
		assert.LessOrEqual(t, row.minDaemonProtocol, row.protocol, "ledger row %d floors MinDaemonProtocol above the protocol itself", row.protocol)
	}
}
