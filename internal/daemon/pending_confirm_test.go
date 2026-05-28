package daemon

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingConfirmStore_AddAndSnapshot(t *testing.T) {
	store := NewPendingConfirmStore()

	pc := &PendingConfirmation{
		ID:        "op-1",
		Operation: "DeleteIssue",
		Tracker:   "jira",
		Key:       "KAN-1",
		Prompt:    "Delete KAN-1?",
		CreatedAt: time.Now(),
		Decision:  make(chan bool, 1),
	}
	store.Add(pc)

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "op-1", snap[0].ID)
	assert.Equal(t, "DeleteIssue", snap[0].Operation)
	assert.Equal(t, "jira", snap[0].Tracker)
	assert.Equal(t, "KAN-1", snap[0].Key)
	assert.Equal(t, "Delete KAN-1?", snap[0].Prompt)
}

func TestPendingConfirmStore_ResolveApproved(t *testing.T) {
	store := NewPendingConfirmStore()

	pc := &PendingConfirmation{
		ID:        "op-2",
		ClientPID: 1000,
		Decision:  make(chan bool, 1),
	}
	store.Add(pc)

	err := store.Resolve("op-2", true, 2000)
	require.NoError(t, err)

	decision := <-pc.Decision
	assert.True(t, decision)
	assert.Equal(t, 0, store.Len())
}

func TestPendingConfirmStore_ResolveRejected(t *testing.T) {
	store := NewPendingConfirmStore()

	pc := &PendingConfirmation{
		ID:        "op-3",
		ClientPID: 1000,
		Decision:  make(chan bool, 1),
	}
	store.Add(pc)

	err := store.Resolve("op-3", false, 2000)
	require.NoError(t, err)

	decision := <-pc.Decision
	assert.False(t, decision)
	assert.Equal(t, 0, store.Len())
}

func TestPendingConfirmStore_ResolveNotFound(t *testing.T) {
	store := NewPendingConfirmStore()

	err := store.Resolve("nonexistent", true, 2000)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
}

func TestPendingConfirmStore_Cleanup(t *testing.T) {
	store := NewPendingConfirmStore()

	old := &PendingConfirmation{
		ID:        "old-op",
		CreatedAt: time.Now().Add(-10 * time.Minute),
		Decision:  make(chan bool, 1),
	}
	recent := &PendingConfirmation{
		ID:        "recent-op",
		CreatedAt: time.Now(),
		Decision:  make(chan bool, 1),
	}
	store.Add(old)
	store.Add(recent)

	store.Cleanup(5 * time.Minute)

	assert.Equal(t, 1, store.Len())

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "recent-op", snap[0].ID)

	// The expired op should have been rejected.
	decision := <-old.Decision
	assert.False(t, decision)
}

func TestPendingConfirmStore_EmptySnapshot(t *testing.T) {
	store := NewPendingConfirmStore()

	snap := store.Snapshot()
	assert.Empty(t, snap)
	assert.NotNil(t, snap)
}

func TestPendingConfirmStore_Len(t *testing.T) {
	store := NewPendingConfirmStore()
	assert.Equal(t, 0, store.Len())

	store.Add(&PendingConfirmation{ID: "a", ClientPID: 1000, Decision: make(chan bool, 1)})
	assert.Equal(t, 1, store.Len())

	store.Add(&PendingConfirmation{ID: "b", ClientPID: 1000, Decision: make(chan bool, 1)})
	assert.Equal(t, 2, store.Len())

	_ = store.Resolve("a", true, 2000)
	assert.Equal(t, 1, store.Len())
}

func TestPendingConfirmStore_SelfApprovalRejected(t *testing.T) {
	store := NewPendingConfirmStore()

	pc := &PendingConfirmation{
		ID:        "op-self",
		ClientPID: 12345,
		Decision:  make(chan bool, 1),
	}
	store.Add(pc)

	// Same PID as requester → rejected.
	err := store.Resolve("op-self", true, 12345)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "approver PID matches requester PID")
	assert.Equal(t, 1, store.Len()) // still pending

	// Different PID → allowed.
	err = store.Resolve("op-self", true, 99999)
	require.NoError(t, err)
	assert.Equal(t, 0, store.Len())
}

func TestPendingConfirmStore_ResolveRejectsZeroPID(t *testing.T) {
	store := NewPendingConfirmStore()
	pc := &PendingConfirmation{
		ID:        "op-zero",
		ClientPID: 1234,
		Decision:  make(chan bool, 1),
	}
	store.Add(pc)

	err := store.Resolve("op-zero", true, 0)
	require.Error(t, err)
	assert.Equal(t, 1, store.Len(), "entry must remain pending when approverPID is rejected")
}

func TestPendingConfirmStore_ResolveRejectsNegativePID(t *testing.T) {
	store := NewPendingConfirmStore()
	pc := &PendingConfirmation{
		ID:        "op-neg",
		ClientPID: 1234,
		Decision:  make(chan bool, 1),
	}
	store.Add(pc)

	err := store.Resolve("op-neg", true, -1)
	require.Error(t, err)
	assert.Equal(t, 1, store.Len())
}

func TestPendingConfirmStore_ResolveTimeout(t *testing.T) {
	store := NewPendingConfirmStore()
	pc := &PendingConfirmation{
		ID:        "op-timeout",
		ClientPID: 1234,
		Decision:  make(chan bool, 1),
	}
	store.Add(pc)

	store.ResolveTimeout("op-timeout")

	decision := <-pc.Decision
	assert.False(t, decision, "ResolveTimeout must deliver a false decision")
	assert.Equal(t, 0, store.Len())
}

func TestPendingConfirmStore_ResolveTimeoutUnknownIDNoop(t *testing.T) {
	store := NewPendingConfirmStore()
	// Must not panic or error for unknown id.
	store.ResolveTimeout("nonexistent")
	assert.Equal(t, 0, store.Len())
}

// Fire many goroutines against Add/Snapshot/Resolve/Len so the race
// detector can see whether the mutex actually guards every path into
// s.ops. Intended to be run with `go test -race`.
func TestPendingConfirmStore_concurrentAccess(t *testing.T) {
	store := NewPendingConfirmStore()
	const workers = 10
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	// Writers add then resolve their own entries with a distinct
	// approver PID so the self-approval guard does not reject them.
	for w := 0; w < workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				id := fmt.Sprintf("w%d-i%d", workerID, i)
				pc := &PendingConfirmation{
					ID:        id,
					ClientPID: 1000 + workerID,
					CreatedAt: time.Now(),
					Decision:  make(chan bool, 1),
				}
				store.Add(pc)
				_ = store.Resolve(id, true, 9999)
			}
		}(w)
	}

	// Readers walk the store via Snapshot and Len.
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = store.Snapshot()
				_ = store.Len()
			}
		}()
	}

	wg.Wait()
	assert.Equal(t, 0, store.Len(), "all entries should have been resolved")
}
