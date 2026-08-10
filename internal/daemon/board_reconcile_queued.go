package daemon

import (
	"context"
	"time"
)

// QueuedLaunchGrace is how long a decided card may sit unstarted before the
// machine starts it. ApplyOption records the choice and then launches in the
// same call, so any real gap is milliseconds; the grace exists only so a
// reconcile tick landing inside that gap does not race a launch already on its
// way and produce two agents for one stage.
const QueuedLaunchGrace = 2 * time.Minute

// reconcileQueuedLaunch starts the stage a recorded decision queued when the
// launch that should have followed it never happened.
//
// This is the pass the queued state did not have. BoardQueued exists so the
// stuck-running sweep will not red a card whose decision was just answered
// (SC-1320), and it achieved that by being a state no sweep looked at — every
// other pass keys on a running card, an outage, a handoff, a PR loop or a live
// container, and a decided-but-unstarted card is none of those. So the window
// meant to protect a card for a few seconds could hold it forever (SC-3865).
//
// It relaunches through relaunchBounded rather than tryRelaunch, and the reason
// is the whole subtlety: tryRelaunch classifies from the stage's last RECORDED
// exit, which for a queued card is typically ExitNeedsInput — the stage asking
// the very question this decision answers. That classifies as "leave it for a
// human", so the ordinary retry path would refuse to start exactly the cards
// this pass exists for. The budget still applies: a card that cannot launch
// burns DefaultStageRetries and is then left alone, and a refusal that starts
// nothing (the plan gate) is not charged at all.
//
// Note the deliberate asymmetry with reconcileOutage, which avoids the bounded
// path for the opposite reason (SC-3024): an outage must never spend the budget
// a real failure needs. A launch that never happened is a real failure — an
// undiagnosed one — so charging it is correct, and the cap is what stops an
// unlaunchable card relaunching forever.
// It posts no note of its own (the nil commenter below), for reconcileOutage's
// reason: the relaunched stage's *-started marker is already the ticket-visible
// record that work resumed, and a pass on a fixed cycle that comments each time
// it acts is how a weekend produced hundreds of them (SC-2851).
func reconcileQueuedLaunch(ctx context.Context, drivable DrivableCards, deps ReconcileDeps, now time.Time) int {
	logger := deps.Logger
	if !deps.Retry.enabled() {
		return 0
	}
	alive, ok := deps.aliveAgents("queued launches")
	if !ok {
		return 0
	}

	launched := 0
	for _, card := range drivable.cards {
		// Read the choice directly rather than deriving the card: this returns the
		// stage the block named AND the comment that chose it, and the comment's
		// age is what separates a launch in flight from one that never came.
		stage, choice, ok := optionChosenQueued(card.Comments)
		if !ok {
			continue
		}
		if now.Sub(choice.Created) < QueuedLaunchGrace {
			continue
		}
		// A sequencing answer queued this stage precisely so it would NOT run yet.
		// The grace above is about a launch in flight; this is about one nobody
		// asked for. Nothing is charged while it waits — the card is doing exactly
		// what it was told to.
		if waitsFor := waitsForOf(choice); waitsFor != "" {
			if !deps.waitCleared(ctx, card.Key, waitsFor) {
				continue
			}
			logger.Info().Str("pm", card.Key).Str("waited for", waitsFor).
				Msg("board reconcile: the ticket this work waited for is done; releasing the stage")
		}
		// A live agent for the stage means the launch did happen and simply has
		// not posted its started marker yet — the same alive-guard every other
		// pass uses, and the difference between a slow launch and a missing one.
		if _, ok := alive[agentNameFor(card.Key, stage)]; ok {
			continue
		}
		if deps.Retry.relaunchBounded(ctx, card.Key, stage,
			"the decision was answered but the launch never started",
			nil, deps.DaemonID, logger) {
			logger.Info().Str("pm", card.Key).Str("stage", string(stage)).
				Msg("board reconcile: started the stage a decision had queued")
			launched++
		}
	}
	return launched
}

// waitCleared reports whether the ticket a sequencing answer deferred to has
// finished, so the stage it held may start.
//
// An answer that cannot be checked reports false — no probe wired, a tracker
// that could not be reached, a key that does not resolve. Holding is the
// direction that cannot undo the decision: starting the work early is the one
// outcome the person ruled out, and there is no bound on how long a deliberate
// wait may last, so an unreadable blocker is not a reason to give up on it. The
// card says what it waits for, and a person can start it by hand at any time.
func (d ReconcileDeps) waitCleared(ctx context.Context, pmKey, waitsFor string) bool {
	if d.ClosedProbe == nil {
		return false
	}
	closed, err := d.ClosedProbe(ctx, waitsFor)
	if err != nil {
		d.Logger.Warn().Err(err).Str("pm", pmKey).Str("waits for", waitsFor).
			Msg("board reconcile: cannot read the ticket this work waits for; keeping it held")
		return false
	}
	return closed
}
