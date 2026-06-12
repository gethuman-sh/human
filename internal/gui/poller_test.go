package gui

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/monitor"
)

type fakeFetcher struct {
	calls atomic.Int64
}

func (f *fakeFetcher) FetchFull(context.Context) *monitor.Snapshot {
	f.calls.Add(1)
	return &monitor.Snapshot{FetchedAt: time.Now()}
}

func TestPoller_SnapshotFetchesOnDemand(t *testing.T) {
	f := &fakeFetcher{}
	p := NewPoller(f)

	dto, err := p.Snapshot(context.Background())
	require.NoError(t, err)
	assert.False(t, dto.FetchedAt.IsZero())
	assert.EqualValues(t, 1, f.calls.Load())

	// A second call inside the staleness window serves the cache.
	_, err = p.Snapshot(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, f.calls.Load())
}

func TestPoller_IdleWithoutClients(t *testing.T) {
	f := &fakeFetcher{}
	p := NewPoller(f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	// Wake the loop explicitly: with no clients attached it must not fetch.
	select {
	case p.wake <- struct{}{}:
	default:
	}
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 0, f.calls.Load())
}

func TestPoller_FetchesWhileClientAttached(t *testing.T) {
	f := &fakeFetcher{}
	p := NewPoller(f)

	var pushes atomic.Int64
	p.SetBroadcast(func(SnapshotDTO) { pushes.Add(1) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	p.ClientAttached()
	defer p.ClientDetached()

	// The attach wake-up triggers an immediate fetch + broadcast.
	require.Eventually(t, func() bool { return pushes.Load() >= 1 }, 2*time.Second, 10*time.Millisecond)
	assert.GreaterOrEqual(t, f.calls.Load(), int64(1))
}

func TestPoller_ClientCountNeverNegative(t *testing.T) {
	p := NewPoller(&fakeFetcher{})
	p.ClientDetached()
	assert.Equal(t, 0, p.clientCount())
}
