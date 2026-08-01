package cmddaemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// A GitHub entry configured for pull requests is not a ticket source. Asked for
// a ticket listing it searches every issue the token can see, which is
// expensive, unrelated to this project's record, and rate limited — observed
// live tripping GitHub's secondary rate limit on every scheduled pass while
// contributing nothing (SC-2132).
func TestTicketSources_SkipsARolelessGitHubEntry(t *testing.T) {
	got := ticketSources([]tracker.Instance{
		{Name: "human", Kind: "github"},   // credentials for the forge
		{Name: "human", Kind: "shortcut"}, // the PM tracker (role inferred)
	})

	names := make([]string, 0, len(got))
	for _, i := range got {
		names = append(names, i.Kind)
	}
	assert.Equal(t, []string{"shortcut"}, names,
		"only trackers carrying this project's work belong in the record")
}

// A team whose tracker IS GitHub declares role: pm, and must be indexed exactly
// as before — the rule is about a declared role, not about the vendor.
func TestTicketSources_KeepsADeclaredTracker(t *testing.T) {
	got := ticketSources([]tracker.Instance{{Name: "work", Kind: "github", Role: "pm"}})

	assert.Len(t, got, 1, "a declared ticket tracker is a ticket source whatever its kind")
}

// Only Shortcut infers a role for free, so a Linear or Jira tracker configured
// without one is the ordinary case, not a forge in disguise. Skipping it would
// leave the whole backlog out of the record — the exact emptiness this work
// exists to remove.
func TestTicketSources_KeepsARolelessNonForgeTracker(t *testing.T) {
	got := ticketSources([]tracker.Instance{
		{Name: "work", Kind: "linear"},
		{Name: "ops", Kind: "jira"},
	})

	assert.Len(t, got, 2, "a backend configured at all is configured because tickets live there")
}

func TestTicketSources_EmptyInputIsEmpty(t *testing.T) {
	assert.Empty(t, ticketSources(nil))
}

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
