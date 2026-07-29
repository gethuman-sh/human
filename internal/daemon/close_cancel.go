package daemon

import (
	"context"

	"github.com/rs/zerolog"
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
// container and worktree, and returns how many it stopped. This is the
// close-is-cancellation mechanism (1698): closing a ticket from the board must
// stop the run it was cancelling, otherwise the agent keeps working invisibly
// against a closed card — holding its container, posting markers to a closed
// ticket, even pushing commits for work the user called off. The reconcile
// safety net cannot reach it (it enumerates open cards only), so the stop has to
// happen here, at the close.
//
// Best-effort by design: a liveness probe that fails leaves the run alone (a
// blip is not evidence of an agent to stop), and a stop that fails is logged and
// the remaining agents are still attempted — mirroring the reconcile pass's
// hung-agent stop. Stopping is idempotent, so a no-longer-present agent is
// harmless.
func StopAgentsForPMKey(ctx context.Context, pmKey string, liveAgents func() ([]string, error), stopAgent func(context.Context, string) error, logger zerolog.Logger) int {
	if liveAgents == nil || stopAgent == nil {
		return 0
	}
	names, err := liveAgents()
	if err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).
			Msg("close-cancel: cannot list live agents, leaving any run untouched")
		return 0
	}
	stopped := 0
	for _, name := range AgentsForPMKey(names, pmKey) {
		if err := stopAgent(ctx, name); err != nil {
			logger.Warn().Err(err).Str("pm", pmKey).Str("agent", name).
				Msg("close-cancel: cannot stop agent for closed ticket")
			continue
		}
		logger.Info().Str("pm", pmKey).Str("agent", name).
			Msg("close-cancel: stopped agent for closed ticket")
		stopped++
	}
	return stopped
}
