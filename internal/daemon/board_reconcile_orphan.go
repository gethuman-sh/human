package daemon

import (
	"context"

	"github.com/rs/zerolog"
)

// ClosedTicketProbe reports whether pmKey's ticket has left the board as
// done/closed. It is the authority the orphan sweep needs because the reconcile
// lister enumerates OPEN cards only: absence from that list is not proof a
// ticket was closed (a flaky per-ticket fetch drops it too), so the sweep never
// stops a run on absence alone — it asks. A nil probe disables the sweep (the
// package's "nil disables" convention).
type ClosedTicketProbe func(ctx context.Context, pmKey string) (bool, error)

// reconcileOrphanedAgents stops board agents still running against a ticket that
// has left the board as done/closed — the orphan the close gate cannot cover
// (1698). Closing FROM the board now stops the run before it transitions, but a
// ticket closed anywhere else (the tracker's own web UI, `human close`, a
// teammate, or a close that predates the gate) never passes through that path,
// and the reconcile net that would otherwise recover the card enumerates open
// cards only. Such an agent would keep working invisibly against a closed
// ticket: holding its container and worktree, posting markers, even pushing
// commits for work the user called off.
//
// It works from the AGENT side rather than the ticket side, so it costs nothing
// on a healthy board: an agent whose key matches an open card is dismissed
// without a tracker call, and only a key with no open card is probed. Every
// uncertainty resolves to "leave it running" — an unparseable name, a probe
// error, or a ticket that is merely absent rather than confirmed closed — because
// killing live work on absent evidence is the one failure this must never risk.
func reconcileOrphanedAgents(ctx context.Context, cards []ReconcileCard, liveAgents LiveAgentLister, closed ClosedTicketProbe, stopAgent StopAgent, postRecord FailedMarkerPoster, logger zerolog.Logger) int {
	if liveAgents == nil || closed == nil || stopAgent == nil {
		return 0
	}
	names, err := liveAgents()
	if err != nil {
		logger.Warn().Err(err).Msg("board reconcile: cannot list live agents for orphan sweep")
		return 0
	}
	// Agent names embed the SANITIZED key, so open cards are matched through the
	// same sanitize() the launcher used — raw-key equality would miss any key
	// carrying characters the name encoding replaced.
	open := make(map[string]struct{}, len(cards))
	for _, card := range cards {
		open[sanitize(card.Key)] = struct{}{}
	}
	candidates, order := orphanCandidates(names, open)

	stopped := 0
	for _, key := range order {
		isClosed, probeErr := closed(ctx, key)
		if probeErr != nil {
			logger.Warn().Err(probeErr).Str("pm", key).
				Msg("board reconcile: cannot confirm ticket is closed, leaving its run alone")
			continue
		}
		if !isClosed {
			continue
		}
		for _, name := range candidates[key] {
			if err := stopAgent(name); err != nil {
				logger.Warn().Err(err).Str("pm", key).Str("agent", name).
					Msg("board reconcile: cannot stop agent orphaned on a closed ticket")
				continue
			}
			logger.Info().Str("pm", key).Str("agent", name).
				Msg("board reconcile: stopped agent orphaned on a closed ticket")
			recordCancelledRun(ctx, key, name, postRecord, logger)
			stopped++
		}
	}
	return stopped
}

// orphanCandidates groups the live board agents whose PM key matches no open
// card, keyed by that PM key, plus the keys in encounter order so the probe
// round-trips (and the log they produce) are deterministic rather than map-order.
// One key can hold several agents — the stages of one card — and they share a
// single probe.
func orphanCandidates(names []string, open map[string]struct{}) (map[string][]string, []string) {
	candidates := make(map[string][]string)
	var order []string
	for _, name := range names {
		key, _, ok := parseAgentName(name)
		if !ok {
			continue
		}
		if _, isOpen := open[key]; isOpen {
			continue
		}
		if _, seen := candidates[key]; !seen {
			order = append(order, key)
		}
		candidates[key] = append(candidates[key], name)
	}
	return candidates, order
}

// recordCancelledRun leaves the trace the stop never left. Closing a ticket
// kills its agents and fires from outside the marker bus, so a card could go
// from running to gone with nothing on the thread saying work had been
// interrupted — 4 of the 382 closed PM tickets measured on this board were
// closed out of a running state exactly that way (SC-4151 E10).
//
// Best-effort by contract, and deliberately AFTER the stop: the stop is the
// safety property this pass exists for, and a tracker that will not take a
// comment — which a just-closed ticket may well refuse — must never leave an
// agent running against called-off work.
func recordCancelledRun(ctx context.Context, pmKey, agentName string, postRecord FailedMarkerPoster, logger zerolog.Logger) {
	if postRecord == nil {
		return
	}
	_, stage, ok := parseAgentName(agentName)
	if !ok {
		return
	}
	if err := postRecord(ctx, pmKey, RunCancelledBody(stage, agentName)); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Str("agent", agentName).
			Msg("board reconcile: stopped the orphaned agent but could not record the cancellation")
	}
}
