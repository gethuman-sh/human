// Package capabilities answers one question for a pipeline agent: what is this
// run actually allowed to do?
//
// It replaces the branch-per-context prompts that made every skill describe two
// worlds at once ("when BOARD_CONTEXT is true, do not push…"). An agent reads
// its capability set and follows one rule instead — attempt nothing the set
// forbids, and treat a missing capability as a boundary rather than a failure.
// A new execution context then needs no prompt edit.
package capabilities

import (
	"context"
	"os/exec"
	"strings"

	"github.com/gethuman-sh/human/internal/env"
)

// Workspace values describe where the checkout the agent works in came from.
const (
	WorkspaceLocal       = "local"
	WorkspaceBindMounted = "bind-mounted"
)

// Set is what a run may do. It is deliberately small: each field answers a
// decision a pipeline stage actually has to make.
type Set struct {
	// BoardContext reports that this run is a board stage agent, which is the
	// reason most capabilities are withheld.
	BoardContext bool   `json:"board_context"`
	CanPush      bool   `json:"can_push"`
	CanOpenPR    bool   `json:"can_open_pr"`
	OwnsDeploy   bool   `json:"owns_deploy"`
	Workspace    string `json:"workspace"`
	Agent        string `json:"agent,omitempty"`
	// Reason states, in one line an agent can quote back, why the set is
	// restricted — so a stage that stops can say what stopped it.
	Reason string `json:"reason,omitempty"`
}

// RemoteProbe reports whether the checkout has a push remote configured.
type RemoteProbe func(ctx context.Context) bool

// Detect resolves the capability set for the current run.
//
// The board signal is the agent name prefix the daemon assigns
// ("board-<KEY>-<stage>"), the same marker internal/daemon keys its failure
// watcher on. A board container holds no push credentials and the board's
// Deploy stage owns shipping, so it may neither push, open a PR, nor deploy.
func Detect(ctx context.Context, probe RemoteProbe) Set {
	agent := env.Lookup(ctx, "HUMAN_AGENT_NAME")
	board := strings.HasPrefix(agent, "board-")

	set := Set{BoardContext: board, Agent: agent, Workspace: WorkspaceLocal}
	if board {
		set.Workspace = WorkspaceBindMounted
		set.Reason = "board stage agent: the container holds no push credentials and the board's Deploy stage ships the work"
		return set
	}

	if probe == nil || !probe(ctx) {
		set.Reason = "no push remote is configured for this checkout"
		return set
	}

	set.CanPush = true
	set.CanOpenPR = true
	set.OwnsDeploy = true
	return set
}

// GitRemoteProbe reports whether git knows a remote to push to. A failure to
// run git at all counts as "no remote": the caller then withholds the
// capability, which is always the safe direction.
func GitRemoteProbe(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "git", "remote").Output() // #nosec G204 -- fixed command, no user input
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
