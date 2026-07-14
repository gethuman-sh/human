package daemon

import (
	"context"
	"strings"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/tracker"
)

// RunBoardFailureWatch watches for SessionEnd-style hook events from board
// agents and posts the stage's *-failed marker when an agent exits WITHOUT
// having posted its stage's done-marker. This closes the gap where an agent
// dies (or is killed) mid-stage: the board would otherwise show a stuck
// spinner forever. It mirrors RunAgentCleanup's subscribe loop.
//
// It is also the seam where the pipeline chains: a build that finishes
// cleanly (handoff posted) flows straight into its review via chainReview —
// no user gesture. Chaining rides the live SessionEnd event, never a
// comment-scan, so pre-existing handoffs are not retroactively reviewed on
// daemon start. nil chainReview disables chaining.
//
// commenterFor resolves the PM-role commenter lazily (per event) so the watcher
// holds no tracker handle across its lifetime; the PM commenter MUST be
// resolved by role, never by key prefix (both trackers may share a name).
func RunBoardFailureWatch(ctx context.Context, store *HookEventStore, commenterFor func() (tracker.Commenter, error), chainReview func(pmKey string) error, logger zerolog.Logger) {
	if store == nil || commenterFor == nil {
		return
	}

	ch := store.Subscribe()
	defer store.Unsubscribe(ch)

	logger.Info().Msg("board failure watcher started")

	handled := make(map[string]bool)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			for _, evt := range store.RecentEvents() {
				if !strings.HasPrefix(evt.AgentName, "board-") {
					continue
				}
				if evt.EventName != "Stop" && evt.EventName != "SessionEnd" && evt.EventName != "StopFailure" {
					continue
				}
				if handled[evt.AgentName] {
					continue
				}
				handled[evt.AgentName] = true
				go handleBoardAgentExit(ctx, evt.AgentName, commenterFor, chainReview, logger)
			}
		}
	}
}

// handleBoardAgentExit posts the stage's *-failed marker unless the stage's
// latest marker is already its done-marker (a clean finish). A cleanly
// finished build chains into its review. Pulled out so the watch loop stays a
// thin event dispatcher.
func handleBoardAgentExit(ctx context.Context, agentName string, commenterFor func() (tracker.Commenter, error), chainReview func(pmKey string) error, logger zerolog.Logger) {
	pmKey, stage, ok := parseAgentName(agentName)
	if !ok {
		return
	}
	commenter, err := commenterFor()
	if err != nil {
		logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot resolve PM commenter")
		return
	}
	comments, err := commenter.ListComments(ctx, pmKey)
	if err != nil {
		logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot list comments")
		return
	}
	// A clean stage finish leaves the stage's done-marker as the latest marker;
	// only treat the exit as a failure when that did NOT happen.
	if _, state := latestStageState(comments, stage); state == BoardDone {
		if stage == BoardImplementation && chainReview != nil {
			if err := chainReview(pmKey); err != nil {
				logger.Warn().Err(err).Str("pm", pmKey).Msg("board chain: cannot start review after build")
			}
		}
		return
	}
	body := failedHeaderFor(stage) + "\nagent exited without completing the stage"
	if _, err := commenter.AddComment(ctx, pmKey, body); err != nil {
		logger.Warn().Err(err).Str("agent", agentName).Msg("board failure: cannot post failed marker")
	}
}
