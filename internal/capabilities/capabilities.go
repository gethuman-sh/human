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

// boardPRFixStage is the agent-name suffix the daemon assigns the PR-review→fix
// loop's fixer ("board-<KEY>-prfix"). It is the one board stage that must push:
// it commits the reviewer's requested change to its own PR branch, and the
// reviewer then re-reads the pushed head — a fixer that cannot push deadlocks
// the loop (SC-1760). Kept as a local constant rather than imported from
// internal/daemon so this package stays dependency-free of the daemon.
const boardPRFixStage = "prfix"

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
		return detectBoard(ctx, probe, set)
	}

	if probe == nil || !probe(ctx) {
		set.Reason = "this checkout has no reachable remote — either none is configured, or its credentials do not authenticate"
		return set
	}

	set.CanPush = true
	set.CanOpenPR = true
	set.OwnsDeploy = true
	return set
}

// detectBoard resolves the capability set for a board stage agent. Every board
// stage is credential-restricted except the PR-review→fix loop's fixer, which
// must push its own PR branch so the reviewer re-reads a head that carries the
// fix rather than the stale pre-fix head the loop would otherwise spin on
// (SC-1760). The fixer still may neither open a PR nor deploy — those stay the
// board's Deploy stage — and its push is gated on the same reachable-remote
// probe a standalone run passes, so a container with no usable credentials
// still withholds push (a withheld capability is a boundary, never a failure).
func detectBoard(ctx context.Context, probe RemoteProbe, set Set) Set {
	if boardStage(set.Agent) != boardPRFixStage {
		set.Reason = "board stage agent: the container holds no push credentials and the board's Deploy stage ships the work"
		return set
	}
	if probe == nil || !probe(ctx) {
		set.Reason = "board PR-fixer: no reachable remote to push its PR branch to — either none is configured, or its credentials do not authenticate"
		return set
	}
	set.CanPush = true
	set.Reason = "board PR-fixer: may push its own PR branch so the review→fix loop converges; opening PRs and deploying remain the Deploy stage's"
	return set
}

// boardStage returns the stage token of a board agent name ("board-<KEY>-<stage>"),
// which is everything after the final hyphen. An empty string for a name that
// carries no stage suffix keeps a malformed name out of the fixer carve-out.
func boardStage(agent string) string {
	idx := strings.LastIndex(agent, "-")
	if idx < 0 || idx == len(agent)-1 {
		return ""
	}
	return agent[idx+1:]
}

// GitRemoteProbe reports whether this checkout can actually reach its remote
// with the credentials it has.
//
// Checking that a remote is merely *configured* is not the same question and
// was the wrong one: a container with an origin but no usable credentials
// answered "yes" and the run discovered the truth at the push, after all the
// work was done. `git ls-remote` costs one network round trip and proves the
// remote resolves and authenticates. It does not prove write permission — no
// read-only check can — so a push may still be refused; what it removes is the
// common case of having no credentials at all.
//
// Any failure counts as "cannot reach": withholding a capability is always the
// safe direction, since a withheld capability is a boundary while a wrongly
// granted one is a run that fails at the last step.
func GitRemoteProbe(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "git", "remote").Output() // #nosec G204 -- fixed command, no user input
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return false
	}
	// --exit-code makes an empty (but reachable) remote succeed rather than
	// look like a failure; a fresh repository with no branches is still pushable.
	return exec.CommandContext(ctx, "git", "ls-remote", "--quiet", "origin").Run() == nil // #nosec G204 -- fixed command, no user input
}
