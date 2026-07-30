package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

// A peer daemon's live card, seen by a SECOND daemon whose local liveAgents is
// empty, must be spared: past the 15m local grace but under the 2h foreign
// grace, the foreign owner's healthy run is invisible here and must not be
// reddened (SC-1450 / SC-1387).
func TestReconcileStuckRunning_SparesForeignCardUnderForeignGrace(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{cmt(
			StampDaemon(ImplementationStartedHeader, "peerAAAA"),
			now.Add(-StuckRunningGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), cards, liveAgents(),
		capturingPoster(&posted), alwaysReachable, StageRetry{}, nil, nil, "thisBBBB", now, zerolog.Nop())

	assert.Equal(t, 0, n, "a foreign-owned card under the foreign grace must not be reddened")
	assert.Empty(t, posted)
}

// A foreign-owned card aged past the 2h foreign grace is treated as genuinely
// abandoned (its owner is gone) and still recovers, so a dead peer never
// dead-ends a card forever.
func TestReconcileStuckRunning_RedsForeignCardPastForeignGrace(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{cmt(
			StampDaemon(ImplementationStartedHeader, "peerAAAA"),
			now.Add(-StuckRunningForeignGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), cards, liveAgents(),
		capturingPoster(&posted), alwaysReachable, StageRetry{}, nil, nil, "thisBBBB", now, zerolog.Nop())

	assert.Equal(t, 1, n)
	assert.Len(t, posted, 1)
	assert.True(t, strings.HasPrefix(posted[0].Body, ImplementationFailedHeader))
}

// The owning daemon still polices its OWN card at the 15m local grace with real
// local-liveness evidence (empty liveAgents => its agent is gone), so a
// genuinely dead own-card recovers promptly.
func TestReconcileStuckRunning_RedsOwnCardAtLocalGrace(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{cmt(
			StampDaemon(ImplementationStartedHeader, "thisBBBB"),
			now.Add(-StuckRunningGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), cards, liveAgents(),
		capturingPoster(&posted), alwaysReachable, StageRetry{}, nil, nil, "thisBBBB", now, zerolog.Nop())

	assert.Equal(t, 1, n)
	assert.Len(t, posted, 1)
}
