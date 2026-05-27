package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmddaemon"
)

func TestBuildChromeBridgeCmd_Exists(t *testing.T) {
	cmd := cmddaemon.BuildChromeBridgeCmd("")
	assert.Equal(t, "chrome-bridge", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestBuildChromeBridgeCmd_ForegroundFlag(t *testing.T) {
	cmd := cmddaemon.BuildChromeBridgeCmd("")

	fg := cmd.Flags().Lookup("foreground")
	require.NotNil(t, fg, "expected --foreground flag to exist")
	assert.Equal(t, "false", fg.DefValue, "expected --foreground to default to false")
}

func TestChromeBridge_MissingAddr(t *testing.T) {
	t.Setenv("HUMAN_CHROME_ADDR", "")
	t.Setenv("HUMAN_DAEMON_TOKEN", "some-token")

	cmd := cmddaemon.BuildChromeBridgeCmd("")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUMAN_CHROME_ADDR")
}

func TestChromeBridge_MissingToken(t *testing.T) {
	t.Setenv("HUMAN_CHROME_ADDR", "localhost:19286")
	t.Setenv("HUMAN_DAEMON_TOKEN", "")

	cmd := cmddaemon.BuildChromeBridgeCmd("")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUMAN_DAEMON_TOKEN")
}

func TestChromeBridge_MissingAddr_Foreground(t *testing.T) {
	t.Setenv("HUMAN_CHROME_ADDR", "")
	t.Setenv("HUMAN_DAEMON_TOKEN", "some-token")

	cmd := cmddaemon.BuildChromeBridgeCmd("")
	cmd.SetArgs([]string{"--foreground"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUMAN_CHROME_ADDR")
}

func TestChromeBridge_MissingToken_Foreground(t *testing.T) {
	t.Setenv("HUMAN_CHROME_ADDR", "localhost:19286")
	t.Setenv("HUMAN_DAEMON_TOKEN", "")

	cmd := cmddaemon.BuildChromeBridgeCmd("")
	cmd.SetArgs([]string{"--foreground"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HUMAN_DAEMON_TOKEN")
}

func TestChromeBridge_RegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "chrome-bridge" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected chrome-bridge command to be registered")
}

func TestChromeBridgeLogPath(t *testing.T) {
	p := cmddaemon.ChromeBridgeLogPath()
	assert.Contains(t, p, "chrome-bridge.log")
	assert.Contains(t, p, ".human")
}
