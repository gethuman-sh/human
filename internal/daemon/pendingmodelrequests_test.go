package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPendingModelRequests_HoldThenReplay(t *testing.T) {
	p := NewPendingModelRequests()
	inflight := NewInflightModelRequests()
	p.Hold("172.17.0.9", 1)

	p.Replay("172.17.0.9", "board-SC-1-implementation", inflight)

	assert.True(t, inflight.Outstanding("board-SC-1-implementation"))
}

func TestPendingModelRequests_ReplayClearsTheHold(t *testing.T) {
	p := NewPendingModelRequests()
	inflight := NewInflightModelRequests()
	p.Hold("172.17.0.9", 1)

	p.Replay("172.17.0.9", "board-SC-1-implementation", inflight)
	// A second replay must not double-apply the same mark.
	inflight2 := NewInflightModelRequests()
	p.Replay("172.17.0.9", "board-SC-1-implementation", inflight2)

	assert.False(t, inflight2.Outstanding("board-SC-1-implementation"), "a held mark is consumed once")
}

func TestPendingModelRequests_PruneDropsStaleHolds(t *testing.T) {
	p := NewPendingModelRequests()
	p.Hold("172.17.0.9", 1)

	p.mu.Lock()
	mark := p.byHost["172.17.0.9"]
	mark.markedAt = time.Now().Add(-time.Hour)
	p.byHost["172.17.0.9"] = mark
	p.mu.Unlock()

	p.Prune(30 * time.Minute)

	inflight := NewInflightModelRequests()
	p.Replay("172.17.0.9", "board-SC-1-implementation", inflight)
	assert.False(t, inflight.Outstanding("board-SC-1-implementation"), "a pruned hold must not replay")
}

func TestPendingModelRequests_PruneKeepsFreshHolds(t *testing.T) {
	p := NewPendingModelRequests()
	p.Hold("172.17.0.9", 1)

	p.Prune(30 * time.Minute)

	inflight := NewInflightModelRequests()
	p.Replay("172.17.0.9", "board-SC-1-implementation", inflight)
	assert.True(t, inflight.Outstanding("board-SC-1-implementation"))
}

func TestPendingModelRequests_NilSafe(t *testing.T) {
	var p *PendingModelRequests
	assert.NotPanics(t, func() {
		p.Hold("172.17.0.9", 1)
		p.Replay("172.17.0.9", "name", NewInflightModelRequests())
		p.Prune(time.Minute)
	})
}
