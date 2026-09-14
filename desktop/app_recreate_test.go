//go:build wailsapp

package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
)

// stubDaemonAt answers one request per connection with the given response, and
// points ~/.human/daemon.json at it so the App's own discovery finds it.
func stubDaemonAt(t *testing.T, resp daemon.Response) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				if !bufio.NewScanner(conn).Scan() {
					return
				}
				_ = json.NewEncoder(conn).Encode(resp)
			}()
		}
	}()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".human"), 0o700))
	data, err := json.Marshal(daemon.DaemonInfo{Addr: ln.Addr().String()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".human", "daemon.json"), data, 0o600))
}

// A recreate that the daemon refused must reach the board's error banner with
// the daemon's own words — the drafter writes nothing in that case, and a
// swallowed error is indistinguishable from a rewrite that produced nothing.
func TestRecreateDescription_SurfacesTheDaemonError(t *testing.T) {
	stubDaemonAt(t, daemon.Response{Stderr: "description recreation not available", ExitCode: 1})

	err := (&App{}).RecreateDescription("SC-1", "a promoted ticket")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "description recreation not available")
}

func TestRecreateDescription_SucceedsOnceTheAgentIsLaunched(t *testing.T) {
	stubDaemonAt(t, daemon.Response{Stdout: "ok\n"})

	assert.NoError(t, (&App{}).RecreateDescription("SC-1", "a promoted ticket"))
}
