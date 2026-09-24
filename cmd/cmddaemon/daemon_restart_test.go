package cmddaemon

import (
	"bytes"
	"net"
	"regexp"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmddoctor"
	"github.com/gethuman-sh/human/internal/daemon"
)

// The regression this file guards against (SC-5397): the protocol-too-old
// refusal and the doctor check both told the operator to run
// `human daemon restart`, a subcommand BuildDaemonCmd never registered — so
// the remedy the refusal itself printed was refused too, leaving `kill` as
// the only way out.

// TestBuildDaemonCmd_RegistersRestart is the direct regression test: it fails
// exactly the way `human daemon restart` used to (unknown command) whenever
// restart is missing from the command tree.
func TestBuildDaemonCmd_RegistersRestart(t *testing.T) {
	root := BuildDaemonCmd(func() *cobra.Command { return &cobra.Command{} }, "test")
	found, _, err := root.Find([]string{"restart"})
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "restart", found.Name())
}

// restartCmd flags mirror both halves it delegates to (stop's force/wait,
// start's addr/chrome-addr/proxy-addr/safe/debug/project) rather than
// inventing a narrower surface.
func TestDaemonRestartCmd_Flags(t *testing.T) {
	cmd := buildDaemonRestartCmd()
	for _, name := range []string{"addr", "chrome-addr", "proxy-addr", "safe", "debug", "project", "force", "wait"} {
		assert.NotNil(t, cmd.Flags().Lookup(name), "expected --%s flag", name)
	}
}

// restart must fully stop whatever is currently running before it starts a
// replacement — never overlap the two, or two daemons briefly hold the same
// ports/PID file.
func TestDaemonRestart_StopsBeforeStarting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pid := livePID(t)
	require.NoError(t, WritePidFile(pid))

	origInFlight := inFlightOps
	inFlightOps = func() (int, bool) { return 0, true }
	t.Cleanup(func() { inFlightOps = origInFlight })

	var startedAfterStop bool
	origStart := startDaemonBackground
	startDaemonBackground = func(_ *cobra.Command, _, _, _ string, _, _ bool, _ []string) error {
		startedAfterStop = !isProcessAlive(pid)
		return nil
	}
	t.Cleanup(func() { startDaemonBackground = origStart })

	var out bytes.Buffer
	cmd := buildDaemonRestartCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.NoError(t, cmd.Execute())

	assert.Contains(t, out.String(), "Daemon stopped")
	assert.True(t, startedAfterStop, "start must run only after the old process is gone")
}

// remedyCommandRe pulls the `human daemon <word>` command name a remedy tells
// the operator to run, so it can be checked against the tree BuildDaemonCmd
// actually registers rather than trusted on the strength of the prose.
var remedyCommandRe = regexp.MustCompile(`human daemon ([a-z]+)`)

func assertRemedyNamesARegisteredDaemonCommand(t *testing.T, remedy string) {
	t.Helper()
	m := remedyCommandRe.FindStringSubmatch(remedy)
	require.NotEmpty(t, m, "remedy %q does not name a `human daemon <word>` command to check", remedy)

	root := BuildDaemonCmd(func() *cobra.Command { return &cobra.Command{} }, "test")
	_, _, err := root.Find([]string{m[1]})
	assert.NoError(t, err, "remedy names `human daemon %s`, which BuildDaemonCmd does not register", m[1])
}

// TestProtocolGateRemedy_NamesARegisteredCommand pins the version-gate
// refusal's remedy to the real command tree: a future reword that names a
// command still missing from BuildDaemonCmd fails here instead of shipping.
func TestProtocolGateRemedy_NamesARegisteredCommand(t *testing.T) {
	if daemon.MinDaemonProtocol <= 1 {
		t.Skip("no rejectable protocol below MinDaemonProtocol")
	}
	err := daemon.DaemonProtocolError(daemon.DaemonInfo{Protocol: daemon.MinDaemonProtocol - 1})
	require.Error(t, err)
	assertRemedyNamesARegisteredDaemonCommand(t, err.Error())
}

// TestDoctorProtocolGateRemedy_NamesARegisteredCommand does the same for the
// doctor check's remedy, exercised through the real `human doctor` command
// against a reachable-but-stale daemon.
func TestDoctorProtocolGateRemedy_NamesARegisteredCommand(t *testing.T) {
	if daemon.MinDaemonProtocol <= 1 {
		t.Skip("no rejectable protocol below MinDaemonProtocol")
	}
	t.Setenv("HOME", t.TempDir())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	require.NoError(t, daemon.WriteInfo(daemon.DaemonInfo{
		Addr:     ln.Addr().String(),
		Protocol: daemon.MinDaemonProtocol - 1,
	}))
	t.Cleanup(daemon.RemoveInfo)

	var out bytes.Buffer
	cmd := cmddoctor.BuildDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	_ = cmd.Execute() // expected to error (protocol gate); the remedy it printed is what matters

	assertRemedyNamesARegisteredDaemonCommand(t, out.String())
}
