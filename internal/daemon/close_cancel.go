package daemon

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// AgentsForPMKey returns the board-agent names in names that belong to pmKey,
// across every stage. An agent name embeds the sanitized PM key (see
// agentNameFor), so a live agent is matched by the same sanitize() the launcher
// used — not by raw-key equality, which would miss keys carrying characters the
// name-encoding replaced. Names that are not board agents (or are malformed) are
// skipped.
func AgentsForPMKey(names []string, pmKey string) []string {
	want := sanitize(pmKey)
	var out []string
	for _, name := range names {
		key, _, ok := parseAgentName(name)
		if !ok {
			continue
		}
		if key == want {
			out = append(out, name)
		}
	}
	return out
}

// StopAgentsForPMKey stops every live board agent claiming pmKey, releasing its
// container and worktree, and returns how many it stopped plus whether the stop
// is fully confirmed. This is the close-is-cancellation mechanism (1698):
// closing a ticket from the board must stop the run it was cancelling, otherwise
// the agent keeps working invisibly against a closed card — holding its
// container, posting markers to a closed ticket, even pushing commits for work
// the user called off. The reconcile safety net cannot reach it (it enumerates
// open cards only), so the stop has to happen here, at the close.
//
// The returned error is the close gate (1698 criterion 2): the caller must NOT
// transition the ticket to done while it is non-nil, so the card stays open and
// thus reachable by the reconcile safety net rather than closing over a run that
// refused to die. It is non-nil when the stop could not be confirmed for every
// agent — either the liveness probe failed (we cannot even enumerate the runs to
// stop them) or a stop attempt itself failed. Every agent is still attempted
// before returning, mirroring the reconcile pass's hung-agent stop, and stopping
// is idempotent so a no-longer-present agent is harmless. A successful clean stop
// (including the common "no agent claims this key") returns a nil error, which
// lets the close proceed.
func StopAgentsForPMKey(ctx context.Context, pmKey string, liveAgents func() ([]string, error), stopAgent func(context.Context, string) error, logger zerolog.Logger) (int, error) {
	if liveAgents == nil || stopAgent == nil {
		return 0, nil
	}
	names, err := liveAgents()
	if err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).
			Msg("close-cancel: cannot list live agents, keeping the card open so the reconcile net can reach any run")
		return 0, errors.WithDetails("close-cancel: cannot list live agents to confirm the run is stopped", "pm", pmKey, "cause", err.Error())
	}
	stopped, failed := 0, 0
	for _, name := range AgentsForPMKey(names, pmKey) {
		if err := stopAgent(ctx, name); err != nil {
			logger.Warn().Err(err).Str("pm", pmKey).Str("agent", name).
				Msg("close-cancel: cannot stop agent for closed ticket, keeping the card open")
			failed++
			continue
		}
		logger.Info().Str("pm", pmKey).Str("agent", name).
			Msg("close-cancel: stopped agent for closed ticket")
		stopped++
	}
	if failed > 0 {
		return stopped, errors.WithDetails("close-cancel: could not stop every agent claiming the ticket", "pm", pmKey, "stopped", stopped, "failed", failed)
	}
	return stopped, nil
}
