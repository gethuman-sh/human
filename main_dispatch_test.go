package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
)

// The protocol gate refuses FORWARDING, not running. Before SC-5397 it fired at
// the top of main, so `human daemon stop` — the remedy the refusal itself names —
// exited 1 against the daemon it was meant to stop, leaving `kill` as the only
// way out.
func TestDecideDispatch_staleDaemonRefusesOnlyForwardedCommands(t *testing.T) {
	if daemon.MinDaemonProtocol <= 1 {
		t.Skip("no rejectable protocol below MinDaemonProtocol")
	}
	stale := daemon.DaemonInfo{Addr: "127.0.0.1:19285", Protocol: daemon.MinDaemonProtocol - 1}

	exempt := [][]string{
		{"daemon", "stop"},
		{"daemon", "restart"},
		{"daemon", "status"},
		{"daemon", "start"},
		{"--tracker", "work", "daemon", "stop"},
		{"doctor"},
		{"doctor", "toolchain"},
		{"--help"},
		{"-h"},
		{"help"},
		{"--version"},
		{"-v"},
		{}, // bare `human` prints the command surface
	}
	for _, args := range exempt {
		d := decideDispatch(args, stale)
		require.NoError(t, d.refuse, "args %v must not be refused by the protocol gate", args)
		assert.False(t, d.forward, "args %v must run locally against a stale daemon", args)
	}

	forwarded := [][]string{
		{"get", "SC-1"},
		{"shortcut", "issue", "get", "SC-1"},
		{"state", "get", "SC-1", "stage.fix"},
	}
	for _, args := range forwarded {
		d := decideDispatch(args, stale)
		require.Error(t, d.refuse, "args %v must still be refused", args)
		assert.True(t, daemon.IsProtocolError(d.refuse), "args %v: refusal must be the protocol error", args)
		assert.False(t, d.forward, "args %v must not be forwarded once refused", args)
	}
}

// A daemon this client may talk to changes nothing: forwarded commands forward,
// local ones stay local, and help is answered by the daemon as before.
func TestDecideDispatch_currentDaemonForwardsAsBefore(t *testing.T) {
	ok := daemon.DaemonInfo{Addr: "127.0.0.1:19285", Protocol: daemon.MinDaemonProtocol}

	d := decideDispatch([]string{"get", "SC-1"}, ok)
	assert.True(t, d.forward)
	assert.NoError(t, d.refuse)

	d = decideDispatch([]string{"--help"}, ok)
	assert.True(t, d.forward)
	assert.NoError(t, d.refuse)

	d = decideDispatch([]string{"daemon", "stop"}, ok)
	assert.False(t, d.forward)
	assert.NoError(t, d.refuse)

	// `feedback` must never be forwarded as raw argv: the daemon's own
	// "feedback" route expects a single marshaled JSON request, and a
	// forwarded KEY/STAGE argv is rejected as the wrong shape (SC-6016).
	d = decideDispatch([]string{"feedback", "SC-1", "planning"}, ok)
	assert.False(t, d.forward)
	assert.NoError(t, d.refuse)
}

// Daemons predating protocol advertising are accepted by every client; refusing
// them would strand everyone during the transition.
func TestDecideDispatch_daemonWithoutProtocolIsNotRefused(t *testing.T) {
	d := decideDispatch([]string{"get", "SC-1"}, daemon.DaemonInfo{Addr: "127.0.0.1:19285"})
	assert.True(t, d.forward)
	assert.NoError(t, d.refuse)
}

func TestIsHelpRequest(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{}, true},
		{[]string{"--help"}, true},
		{[]string{"-h"}, true},
		{[]string{"help"}, true},
		{[]string{"help", "get"}, true},
		{[]string{"get", "SC-1", "--help"}, true},
		{[]string{"--tracker", "work"}, true},
		{[]string{"--tracker", "work", "get", "SC-1"}, false},
		{[]string{"get", "SC-1"}, false},
		{[]string{"--", "--help"}, false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, isHelpRequest(tt.args), "isHelpRequest(%v)", tt.args)
	}
}
