package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// A running stage stamped by a PEER daemon is bound to that machine: this daemon's
// machine-local liveAgents() cannot see the peer's healthy run, so it must never
// red the card — not now, not ever, regardless of how long it has sat. Ownership
// ends by finishing the work or by explicit release, never by a distant machine's
// timeout (SC-2047). The card never reaches the pass because the forTakeover gate
// excludes a foreign-owned stage by identity, which is what lets the delay-only
// StuckRunningForeignGrace be retired rather than merely lengthened.
func TestReconcileStuckRunning_NeverTakesOverAForeignOwnedStage(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	foreign := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{cmt(
			marker.Sign(ImplementationStartedHeader, "peerAAAA", ""),
			// Aged well past any historical grace: elapsed time must not matter.
			now.Add(-72*time.Hour))},
	}}
	var posted []struct{ Key, Body string }

	n := reconcileStuckRunning(context.Background(), takeoverSetAs(foreign, alwaysReachable, "thisBBBB"),
		liveAgents(), capturingPoster(&posted), StageRetry{}, nil, nil, "thisBBBB", now, zerolog.Nop())

	assert.Equal(t, 0, n, "a stage owned by a peer daemon is never reddened from a distance")
	assert.Empty(t, posted, "no failed marker may be posted for a peer machine's running stage")
}

// The owning daemon still polices its OWN running stage at the local grace with
// real machine-local liveness evidence (empty liveAgents => its agent is gone),
// so a genuinely dead own-card recovers promptly. The gate admits it because its
// stamp matches this daemon's id.
func TestReconcileStuckRunning_RedsOwnCardAtLocalGrace(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	own := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{cmt(
			marker.Sign(ImplementationStartedHeader, "thisBBBB", ""),
			now.Add(-StuckRunningGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }

	n := reconcileStuckRunning(context.Background(), takeoverSetAs(own, alwaysReachable, "thisBBBB"),
		liveAgents(), capturingPoster(&posted), StageRetry{}, nil, nil, "thisBBBB", now, zerolog.Nop())

	assert.Equal(t, 1, n)
	assert.Len(t, posted, 1)
}

// An UNSTAMPED running stage (a single-daemon board, or a legacy marker predating
// daemon stamping) has no owner to bind to, so it stays this machine's to red at
// the local grace — the backward-compatible path that keeps single-daemon boards
// unchanged.
func TestReconcileStuckRunning_RedsUnstampedCardAtLocalGrace(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	unstamped := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{cmt(
			ImplementationStartedHeader,
			now.Add(-StuckRunningGrace-time.Minute))},
	}}
	var posted []struct{ Key, Body string }

	n := reconcileStuckRunning(context.Background(), takeoverSetAs(unstamped, alwaysReachable, "thisBBBB"),
		liveAgents(), capturingPoster(&posted), StageRetry{}, nil, nil, "thisBBBB", now, zerolog.Nop())

	assert.Equal(t, 1, n, "an unstamped stage has no owner and stays takeable")
	assert.Len(t, posted, 1)
}
