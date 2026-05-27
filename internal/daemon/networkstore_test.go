package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/proxy"
)

// Compile-time assertion that the store satisfies the proxy emitter
// interface. Keeps the two packages in sync without a runtime cost.
var _ proxy.NetworkEventEmitter = (*NetworkEventStore)(nil)

// fakeClock returns a monotonic fake now() so dedup timestamps are
// deterministic across test runs.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func TestNetworkEventStore_AppendAndSnapshot(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0).UTC())
	store := NewNetworkEventStoreWithClock(clock.Now)

	store.Emit("proxy", "forward", "github.com")

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "proxy", snap[0].Source)
	assert.Equal(t, "forward", snap[0].Status)
	assert.Equal(t, "github.com", snap[0].Host)
	assert.Equal(t, 1, snap[0].Count)
	assert.Equal(t, clock.Now(), snap[0].LastSeen)
}

func TestNetworkEventStore_ConsecutiveDedup(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0).UTC())
	store := NewNetworkEventStoreWithClock(clock.Now)

	for i := 0; i < 47; i++ {
		store.Emit("proxy", "forward", "github.com")
		clock.Advance(time.Second)
	}

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, 47, snap[0].Count)
	// LastSeen reflects the final tick (one second before the current
	// clock because Advance happens after the last Emit).
	expected := time.Unix(1_700_000_000, 0).UTC().Add(46 * time.Second)
	assert.Equal(t, expected, snap[0].LastSeen)
}

func TestNetworkEventStore_NonConsecutiveKeepsRows(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0).UTC())
	store := NewNetworkEventStoreWithClock(clock.Now)

	store.Emit("proxy", "forward", "A")
	store.Emit("proxy", "forward", "B")
	store.Emit("proxy", "forward", "A")

	snap := store.Snapshot()
	require.Len(t, snap, 3)
	assert.Equal(t, "A", snap[0].Host)
	assert.Equal(t, "B", snap[1].Host)
	assert.Equal(t, "A", snap[2].Host)
	for _, e := range snap {
		assert.Equal(t, 1, e.Count)
	}
}

func TestNetworkEventStore_DifferentSourceBreaksDedup(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0).UTC())
	store := NewNetworkEventStoreWithClock(clock.Now)

	store.Emit("proxy", "forward", "x")
	store.Emit("fail", "dial-fail", "x")

	snap := store.Snapshot()
	require.Len(t, snap, 2)
	assert.Equal(t, "proxy", snap[0].Source)
	assert.Equal(t, "fail", snap[1].Source)
}

func TestNetworkEventStore_RingBufferCap(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0).UTC())
	store := NewNetworkEventStoreWithClock(clock.Now)

	total := 250
	for i := 0; i < total; i++ {
		// Unique host per iteration so dedup does not collapse rows.
		store.Emit("proxy", "forward", hostName(i))
	}

	snap := store.Snapshot()
	require.Len(t, snap, maxNetworkEvents)
	// Oldest entries (0..total-maxNetworkEvents-1) should be dropped.
	assert.Equal(t, hostName(total-maxNetworkEvents), snap[0].Host)
	assert.Equal(t, hostName(total-1), snap[len(snap)-1].Host)
}

func TestNetworkEventStore_ConcurrentEmit(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0).UTC())
	store := NewNetworkEventStoreWithClock(clock.Now)

	const workers = 100
	const iter = 100

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iter; i++ {
				store.Emit("proxy", "forward", hostName(id*1000+i))
			}
		}(w)
	}
	wg.Wait()

	// No assertions on ordering (goroutine scheduling is non-deterministic),
	// only that the store is consistent and bounded.
	snap := store.Snapshot()
	assert.LessOrEqual(t, len(snap), maxNetworkEvents)
}

func TestNetworkEventStore_ClockInjection(t *testing.T) {
	fixed := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	store := NewNetworkEventStoreWithClock(func() time.Time { return fixed })

	store.Emit("proxy", "forward", "github.com")

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, fixed, snap[0].LastSeen)
}

func TestNetworkEventStore_LastSeenRefreshesOnDedup(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0).UTC())
	store := NewNetworkEventStoreWithClock(clock.Now)

	store.Emit("proxy", "forward", "A")
	t0 := clock.Now()
	clock.Advance(5 * time.Second)
	store.Emit("proxy", "forward", "A")
	t1 := clock.Now()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, 2, snap[0].Count)
	assert.Equal(t, t1, snap[0].LastSeen)
	assert.True(t, snap[0].LastSeen.After(t0))
}

func TestNetworkEventStore_NewDefaultsToWallClock(t *testing.T) {
	store := NewNetworkEventStore()
	before := time.Now().UTC()
	store.Emit("proxy", "forward", "github.com")
	after := time.Now().UTC()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.False(t, snap[0].LastSeen.Before(before))
	assert.False(t, snap[0].LastSeen.After(after))
}

func TestNetworkEventStore_EmptySnapshot(t *testing.T) {
	store := NewNetworkEventStore()
	snap := store.Snapshot()
	assert.Empty(t, snap)
}

// hostName returns a stable, unique host for index i.
func hostName(i int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	a := alphabet[i%len(alphabet)]
	return string(a) + "-" + itoa(i) + ".example.com"
}

// itoa is a tiny integer-to-string helper so the tests do not depend
// on strconv just for host names.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
