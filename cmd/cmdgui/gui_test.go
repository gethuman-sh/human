package cmdgui

import (
	"bytes"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
)

type recordingOpener struct {
	opened string
}

func (r *recordingOpener) Open(url string) error {
	r.opened = url
	return nil
}

// withRunningDaemon fakes an alive daemon: PID file pointing at this test
// process plus a daemon.json carrying the GUI address.
func withRunningDaemon(t *testing.T, info daemon.DaemonInfo) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, daemon.WritePidFile(os.Getpid()))
	require.NoError(t, daemon.WriteInfo(info))
}

func testCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	return cmd, &out
}

func TestRunGui_OpensAuthURL(t *testing.T) {
	withRunningDaemon(t, daemon.DaemonInfo{
		Addr: "127.0.0.1:19285", GuiAddr: "127.0.0.1:19288", Token: "tok123", PID: os.Getpid(),
	})

	opener := &recordingOpener{}
	cmd, _ := testCmd()
	require.NoError(t, runGui(cmd, nil, false, opener))
	assert.Equal(t, "http://127.0.0.1:19288/auth?token=tok123", opener.opened)
}

func TestRunGui_NoBrowserPrintsURL(t *testing.T) {
	withRunningDaemon(t, daemon.DaemonInfo{
		Addr: "127.0.0.1:19285", GuiAddr: "127.0.0.1:19288", Token: "tok123", PID: os.Getpid(),
	})

	opener := &recordingOpener{}
	cmd, out := testCmd()
	require.NoError(t, runGui(cmd, nil, true, opener))
	assert.Empty(t, opener.opened)
	assert.Contains(t, out.String(), "http://127.0.0.1:19288/auth?token=tok123")
}

func TestRunGui_OldDaemonWithoutGuiListener(t *testing.T) {
	withRunningDaemon(t, daemon.DaemonInfo{
		Addr: "127.0.0.1:19285", Token: "tok123", PID: os.Getpid(),
	})

	cmd, _ := testCmd()
	err := runGui(cmd, nil, false, &recordingOpener{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GUI listener")
}
