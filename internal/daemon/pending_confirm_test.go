package daemon

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingConfirmStore_SubmitAndSnapshot(t *testing.T) {
	store := NewPendingConfirmStore()

	pc := &PendingConfirmation{
		ID:        "op-1",
		Operation: "DeleteIssue",
		Tracker:   "jira",
		Key:       "KAN-1",
		Prompt:    "Delete KAN-1?",
		CreatedAt: time.Now(),
	}
	store.Submit(pc)

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "op-1", snap[0].ID)
	assert.Equal(t, "DeleteIssue", snap[0].Operation)
	assert.Equal(t, "jira", snap[0].Tracker)
	assert.Equal(t, "KAN-1", snap[0].Key)
	assert.Equal(t, "Delete KAN-1?", snap[0].Prompt)
}

func TestPendingConfirmStore_SubmitIdempotentOnID(t *testing.T) {
	store := NewPendingConfirmStore()

	store.Submit(&PendingConfirmation{ID: "op-1", Key: "KAN-1", ClientPID: 1000})
	_, err := store.Resolve("op-1", false, 2000)
	require.NoError(t, err)

	// A resubmit with the same ID must not resurrect or duplicate the
	// already-decided entry.
	store.Submit(&PendingConfirmation{ID: "op-1", Key: "KAN-1", ClientPID: 1000})
	assert.Equal(t, 1, store.Len())
	got, ok := store.Get("op-1")
	require.True(t, ok)
	assert.Equal(t, ConfirmDenied, got.State)
}

func TestPendingConfirmStore_ResolveApproved(t *testing.T) {
	store := NewPendingConfirmStore()

	store.Submit(&PendingConfirmation{
		ID:        "op-2",
		Operation: "DeleteIssue",
		Tracker:   "jira",
		Key:       "KAN-1",
		ClientPID: 1000,
	})

	pc, err := store.Resolve("op-2", true, 2000)
	require.NoError(t, err)
	assert.Equal(t, ConfirmApproved, pc.State)

	// The grant stays queryable and redeemable until consumed or swept.
	got, ok := store.Get("op-2")
	require.True(t, ok)
	assert.Equal(t, ConfirmApproved, got.State)
	assert.False(t, got.ResolvedAt.IsZero())
}

func TestPendingConfirmStore_ConsumeApprovedGrant(t *testing.T) {
	store := NewPendingConfirmStore()
	store.Submit(&PendingConfirmation{
		ID:        "op-c",
		Operation: "DeleteIssue",
		Tracker:   "jira",
		Key:       "KAN-1",
		ClientPID: 1000,
	})

	// Pending grants cannot be redeemed.
	_, ok := store.Consume("op-c", "DeleteIssue", "jira", "KAN-1")
	assert.False(t, ok)

	_, err := store.Resolve("op-c", true, 2000)
	require.NoError(t, err)

	// A grant only covers the exact operation the user saw in the prompt.
	_, ok = store.Consume("op-c", "DeleteIssue", "jira", "KAN-2")
	assert.False(t, ok)
	_, ok = store.Consume("op-c", "EditIssue", "jira", "KAN-1")
	assert.False(t, ok)

	pc, ok := store.Consume("op-c", "DeleteIssue", "jira", "KAN-1")
	require.True(t, ok)
	assert.Equal(t, "op-c", pc.ID)

	// One-time: the grant is gone after redemption.
	_, ok = store.Consume("op-c", "DeleteIssue", "jira", "KAN-1")
	assert.False(t, ok)
	assert.Equal(t, 0, store.Len())
}

func TestPendingConfirmStore_FindPending(t *testing.T) {
	store := NewPendingConfirmStore()
	store.Submit(&PendingConfirmation{
		ID:        "op-f",
		Operation: "DeleteIssue",
		Tracker:   "jira",
		Key:       "KAN-1",
		ClientPID: 1000,
	})

	pc, ok := store.FindPending("DeleteIssue", "jira", "KAN-1")
	require.True(t, ok)
	assert.Equal(t, "op-f", pc.ID)

	_, ok = store.FindPending("DeleteIssue", "jira", "KAN-2")
	assert.False(t, ok)

	// Resolved entries are no longer open prompts.
	_, err := store.Resolve("op-f", false, 2000)
	require.NoError(t, err)
	_, ok = store.FindPending("DeleteIssue", "jira", "KAN-1")
	assert.False(t, ok)
}

func TestPendingConfirmStore_ResolveRejected(t *testing.T) {
	store := NewPendingConfirmStore()

	store.Submit(&PendingConfirmation{ID: "op-3", ClientPID: 1000})

	pc, err := store.Resolve("op-3", false, 2000)
	require.NoError(t, err)
	assert.Equal(t, ConfirmDenied, pc.State)

	// Denied entries stay queryable so a disconnected client can learn the
	// decision later.
	got, ok := store.Get("op-3")
	require.True(t, ok)
	assert.Equal(t, ConfirmDenied, got.State)
	assert.False(t, got.ResolvedAt.IsZero())
}

func TestPendingConfirmStore_ResolveTwiceFails(t *testing.T) {
	store := NewPendingConfirmStore()
	store.Submit(&PendingConfirmation{ID: "op-4", ClientPID: 1000})

	_, err := store.Resolve("op-4", false, 2000)
	require.NoError(t, err)

	_, err = store.Resolve("op-4", true, 2000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already resolved")
}

func TestPendingConfirmStore_ResolveNotFound(t *testing.T) {
	store := NewPendingConfirmStore()

	_, err := store.Resolve("nonexistent", true, 2000)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
}

func TestPendingConfirmStore_SnapshotExcludesResolved(t *testing.T) {
	store := NewPendingConfirmStore()
	store.Submit(&PendingConfirmation{ID: "a", ClientPID: 1000})
	store.Submit(&PendingConfirmation{ID: "b", ClientPID: 1000})

	_, err := store.Resolve("a", false, 2000)
	require.NoError(t, err)

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "b", snap[0].ID)
	// Both remain stored for status queries.
	assert.Equal(t, 2, store.Len())
}

func TestPendingConfirmStore_Remove(t *testing.T) {
	store := NewPendingConfirmStore()
	store.Submit(&PendingConfirmation{ID: "op-6", ClientPID: 1000})

	store.Remove("op-6")
	assert.Equal(t, 0, store.Len())
	_, ok := store.Get("op-6")
	assert.False(t, ok)
}

func TestPendingConfirmStore_Cleanup(t *testing.T) {
	store := NewPendingConfirmStore()

	store.Submit(&PendingConfirmation{
		ID:        "old-op",
		CreatedAt: time.Now().Add(-25 * time.Hour),
	})
	store.Submit(&PendingConfirmation{
		ID:        "old-resolved",
		ClientPID: 1000,
		CreatedAt: time.Now().Add(-25 * time.Hour),
	})
	store.Submit(&PendingConfirmation{
		ID:        "recent-op",
		CreatedAt: time.Now(),
	})
	_, err := store.Resolve("old-resolved", false, 2000)
	require.NoError(t, err)

	store.Cleanup(ConfirmRetention)

	// Age is the only criterion — pending and resolved entries both expire.
	assert.Equal(t, 1, store.Len())
	_, ok := store.Get("old-op")
	assert.False(t, ok)
	_, ok = store.Get("old-resolved")
	assert.False(t, ok)
	_, ok = store.Get("recent-op")
	assert.True(t, ok)
}

func TestPendingConfirmStore_EmptySnapshot(t *testing.T) {
	store := NewPendingConfirmStore()

	snap := store.Snapshot()
	assert.Empty(t, snap)
	assert.NotNil(t, snap)
}

func TestPendingConfirmStore_SelfApprovalRejected(t *testing.T) {
	store := NewPendingConfirmStore()

	store.Submit(&PendingConfirmation{ID: "op-self", ClientPID: 12345})

	// Same PID as requester → rejected.
	_, err := store.Resolve("op-self", true, 12345)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "approver PID matches requester PID")
	got, _ := store.Get("op-self")
	assert.Equal(t, ConfirmPending, got.State, "entry must remain pending")

	// Different PID → allowed.
	_, err = store.Resolve("op-self", true, 99999)
	require.NoError(t, err)
}

func TestPendingConfirmStore_ResolveRejectsZeroPID(t *testing.T) {
	store := NewPendingConfirmStore()
	store.Submit(&PendingConfirmation{ID: "op-zero", ClientPID: 1234})

	_, err := store.Resolve("op-zero", true, 0)
	require.Error(t, err)
	got, _ := store.Get("op-zero")
	assert.Equal(t, ConfirmPending, got.State, "entry must remain pending when approverPID is rejected")
}

func TestPendingConfirmStore_ResolveRejectsNegativePID(t *testing.T) {
	store := NewPendingConfirmStore()
	store.Submit(&PendingConfirmation{ID: "op-neg", ClientPID: 1234})

	_, err := store.Resolve("op-neg", true, -1)
	require.Error(t, err)
	got, _ := store.Get("op-neg")
	assert.Equal(t, ConfirmPending, got.State)
}

// Fire many goroutines against Submit/Snapshot/Resolve/SetResult/Len so the
// race detector can see whether the mutex actually guards every path into
// s.ops. Intended to be run with `go test -race`.
func TestPendingConfirmStore_concurrentAccess(t *testing.T) {
	store := NewPendingConfirmStore()
	const workers = 10
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	// Writers submit then resolve their own entries with a distinct
	// approver PID so the self-approval guard does not reject them.
	for w := 0; w < workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				id := fmt.Sprintf("w%d-i%d", workerID, i)
				store.Submit(&PendingConfirmation{
					ID:        id,
					ClientPID: 1000 + workerID,
					CreatedAt: time.Now(),
				})
				if _, err := store.Resolve(id, true, 9999); err == nil {
					_, _ = store.Consume(id, "", "", "")
				}
			}
		}(w)
	}

	// Readers walk the store via Snapshot, Get and Len.
	for w := 0; w < workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = store.Snapshot()
				_, _ = store.Get(fmt.Sprintf("w%d-i%d", workerID, i))
				_ = store.Len()
			}
		}(w)
	}

	wg.Wait()
	assert.Equal(t, 0, store.Len(), "every approved grant should have been consumed")
}
