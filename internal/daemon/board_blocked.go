package daemon

import (
	"context"
	"slices"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// gatedByBlockers are the stages that begin work on a ticket. Only these wait
// for a blocker.
//
// The point of a dependency is that two pieces of work do not touch the same
// code at the same time, and both of these produce work-product tied to the
// state of the code: a plan written against a codebase that is about to change
// is wrong, and an implementation is the collision itself. Everything after
// them — review, the PR loop, deploy — is the tail of work that already ran.
// Holding those would strand a finished branch behind a ticket it can no longer
// collide with, which is a worse failure than the one this gate prevents.
var gatedByBlockers = []BoardStage{BoardPlanning, BoardImplementation}

// refuseIfBlocked reports an error when pmKey must wait for work that is still
// open, so a stage that would collide with it never starts.
//
// A probe failure deliberately does NOT hold the card. The gate exists to keep
// two runs off the same code, not to become a second liveness dependency —
// turning a tracker blip into a stalled board would cost more than the
// collision it guards against.
func (d BoardTransitionDeps) refuseIfBlocked(ctx context.Context, pmKey string, stage BoardStage) error {
	if d.BlockedBy == nil || !slices.Contains(gatedByBlockers, stage) {
		return nil
	}
	blockers, err := d.BlockedBy(ctx, pmKey)
	if err != nil {
		d.Logger.Warn().Err(err).Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board stage: cannot read dependencies, starting anyway")
		return nil
	}
	if len(blockers) == 0 {
		return nil
	}
	if cycle := d.cycleAmong(ctx, pmKey, blockers); cycle != "" {
		d.Logger.Error().Str("pm", pmKey).Str("blocker", cycle).
			Msg("board stage: tickets block each other, neither can ever start")
		return errors.WithDetails(
			"tickets wait for each other, so neither can start; remove one of the links",
			"pm", pmKey, "blocker", cycle, "stage", string(stage))
	}
	d.Logger.Info().Str("pm", pmKey).Str("stage", string(stage)).Str("blockers", strings.Join(blockers, ", ")).
		Msg("board stage: work is waiting for a blocker that is still open")
	return errors.WithDetails(
		"this work waits for another ticket that is still open; finish it or remove the link",
		"pm", pmKey, "blockedBy", strings.Join(blockers, ", "), "stage", string(stage))
}

// cycleAmong returns the first blocker that is itself waiting for pmKey, or ""
// when none is.
//
// Two tickets waiting for each other never resolve: each is held by the other,
// so no amount of waiting helps and only a person can decide which one goes
// first. Reporting it is the whole remedy — a cycle is a mistake in the data,
// not a state to sit in.
//
// A blocker whose own dependencies cannot be read is treated as not part of a
// cycle: the ordinary refusal still holds the work, which is the safe direction.
func (d BoardTransitionDeps) cycleAmong(ctx context.Context, pmKey string, blockers []string) string {
	for _, blocker := range blockers {
		theirs, err := d.BlockedBy(ctx, blocker)
		if err != nil {
			continue
		}
		if slices.Contains(theirs, pmKey) {
			return blocker
		}
	}
	return ""
}
