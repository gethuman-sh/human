package cmddaemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The schedule itself, driven rather than re-implemented: an immediate pass,
// then one per interval, with every Nth running full. The previous version of
// this test recomputed the arithmetic and asserted against its own copy, so it
// would have passed even if the loop stopped doing it.
func TestRecallSyncLoop_FirstPassIsDeltaThenEveryNthIsFull(t *testing.T) {
	var mu sync.Mutex
	var passes []bool
	ctx, cancel := context.WithCancel(context.Background())

	go recallSyncLoop(ctx, time.Millisecond, 3, func(full bool) {
		mu.Lock()
		defer mu.Unlock()
		passes = append(passes, full)
		if len(passes) == 7 {
			cancel()
		}
	})

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(passes) >= 7
	}, 2*time.Second, time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	// pass 0 is the immediate startup delta; 3rd and 6th thereafter are full.
	assert.Equal(t, []bool{false, false, false, true, false, false, true}, passes[:7])
}

// A non-positive cadence disables full passes, leaving a delta-only schedule for
// anyone who wants the record kept current without the pruning pass.
func TestRecallSyncLoop_ZeroCadenceNeverRunsFull(t *testing.T) {
	var mu sync.Mutex
	var full int
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go recallSyncLoop(ctx, time.Millisecond, 0, func(isFull bool) {
		mu.Lock()
		defer mu.Unlock()
		if isFull {
			full++
		}
	})

	time.Sleep(50 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, full)
}

// Cancelling stops the loop rather than leaking a goroutine that keeps calling
// a tracker after the daemon is done with it.
func TestRecallSyncLoop_CancelStops(t *testing.T) {
	var mu sync.Mutex
	var calls int
	ctx, cancel := context.WithCancel(context.Background())

	go recallSyncLoop(ctx, time.Millisecond, 3, func(bool) {
		mu.Lock()
		defer mu.Unlock()
		calls++
	})

	time.Sleep(20 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	settled := calls
	mu.Unlock()

	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, settled, calls, "no passes may run after cancellation")
}
