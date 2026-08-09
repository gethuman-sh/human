package cmdstats

import (
	"bytes"
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/stats"
)

func TestBuildStatsCmd_subcommands(t *testing.T) {
	cmd := BuildStatsCmd()
	assert.Equal(t, "stats", cmd.Name())
	assert.Equal(t, "utility", cmd.GroupID)

	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
		// A GroupID naming a group the parent never registered makes cobra panic
		// rather than error, so the absence is load-bearing, not cosmetic.
		assert.Empty(t, c.GroupID, "subcommand must not name a group its parent has not registered")
	}
	assert.True(t, subs["subagents"], "subagents subcommand registered")
}

func TestIsValidRange(t *testing.T) {
	assert.True(t, isValidRange("24h"))
	assert.True(t, isValidRange("7d"))
	assert.True(t, isValidRange("30d"))
	assert.False(t, isValidRange(""))
	assert.False(t, isValidRange("1y"))
}

// An unknown window must be refused rather than silently answered for a
// different one — the daemon would fall back to 24h without saying so.
func TestSubagentsCmd_rejectsUnknownRange(t *testing.T) {
	cmd := buildSubagentsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--range=1y"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown range")
	assert.NotContains(t, out.String(), noDaemonMsg, "the daemon must not be consulted for an invalid range")
}

func TestRenderTable_empty(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, renderTable(&b, nil))
	assert.Contains(t, b.String(), "no sub-agent spawns recorded")
}

func TestRenderTable_rows(t *testing.T) {
	var b bytes.Buffer
	counts := []stats.SubagentModelCount{
		{SubagentType: "human-planner", Model: "opus", Count: 3},
		{SubagentType: "human-executor", Model: hookevents.ModelInherited, Count: 5},
		{SubagentType: "legacy", Model: "", Count: 1},
	}
	require.NoError(t, renderTable(&b, counts))

	got := b.String()
	assert.Contains(t, got, "SUB-AGENT TYPE")
	assert.Contains(t, got, "human-planner")
	assert.Contains(t, got, "opus")
	assert.Contains(t, got, hookevents.ModelInherited)
	assert.Contains(t, got, notCapturedLabel)
}

func TestModelLabel(t *testing.T) {
	assert.Equal(t, notCapturedLabel, modelLabel(""))
	assert.Equal(t, hookevents.ModelInherited, modelLabel(hookevents.ModelInherited))
	assert.Equal(t, "opus", modelLabel("opus"))
}

// With no daemon the command must guide rather than fail — an unreachable
// daemon is a missing recorder, not an error in the request.
func TestConnectDaemon_unreachable(t *testing.T) {
	withoutDaemon(t)

	var b bytes.Buffer
	c, err := connectDaemon(&b)
	require.Error(t, err)
	assert.Nil(t, c)
	assert.Contains(t, b.String(), noDaemonMsg)
}

// withoutDaemon makes discovery report that there is none, independently of
// whether one is running on the machine executing the test.
func withoutDaemon(t *testing.T) {
	t.Helper()
	original := connect
	connect = func() (*daemon.Client, error) {
		return nil, errors.WithDetails("daemon not reachable")
	}
	t.Cleanup(func() { connect = original })
}

// startFakeDaemon serves one canned daemon Response per connection and points
// discovery at it, so the command's real client path is exercised without a
// running daemon.
func startFakeDaemon(t *testing.T, resp daemon.Response) {
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
			var req daemon.Request
			_ = json.NewDecoder(conn).Decode(&req)
			_ = json.NewEncoder(conn).Encode(resp)
			_ = conn.Close()
		}
	}()

	original := connect
	connect = func() (*daemon.Client, error) {
		return daemon.NewClient(daemon.DaemonInfo{Addr: ln.Addr().String(), Token: "test-token"})
	}
	t.Cleanup(func() { connect = original })
}

func TestSubagentsCmd_rendersTableFromDaemon(t *testing.T) {
	startFakeDaemon(t, daemon.Response{
		Stdout: `[{"subagent_type":"human-planner","model":"opus","count":3}]` + "\n",
	})

	cmd := buildSubagentsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--range=7d"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "human-planner")
	assert.Contains(t, out.String(), "opus")
}

func TestSubagentsCmd_jsonFromDaemon(t *testing.T) {
	startFakeDaemon(t, daemon.Response{
		Stdout: `[{"subagent_type":"human-executor","model":"inherited","count":5}]` + "\n",
	})

	cmd := buildSubagentsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--json"})

	require.NoError(t, cmd.Execute())
	var counts []stats.SubagentModelCount
	require.NoError(t, json.Unmarshal(out.Bytes(), &counts))
	require.Len(t, counts, 1)
	assert.Equal(t, hookevents.ModelInherited, counts[0].Model)
}

// A daemon that answers with a failure must surface as an error, not as an
// empty table that reads like "no spawns".
func TestSubagentsCmd_daemonError(t *testing.T) {
	startFakeDaemon(t, daemon.Response{ExitCode: 1, Stderr: "stats database is locked"})

	cmd := buildSubagentsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to query sub-agent statistics")
}

// With no daemon at all the command guides and exits cleanly.
func TestSubagentsCmd_noDaemon(t *testing.T) {
	withoutDaemon(t)

	cmd := buildSubagentsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), noDaemonMsg)
}

func TestWriteJSON(t *testing.T) {
	var b bytes.Buffer
	counts := []stats.SubagentModelCount{
		{SubagentType: "human-planner", Model: "opus", Count: 3},
	}
	require.NoError(t, writeJSON(&b, counts))

	var round []stats.SubagentModelCount
	require.NoError(t, json.Unmarshal(b.Bytes(), &round))
	assert.Equal(t, counts, round)
}
