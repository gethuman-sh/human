package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Reopen tests need a real file: reopening ":memory:" would yield a fresh,
// empty database and prove nothing about surviving a restart.
func testConfirmDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "confirms.db")
}

func newTestConfirmDB(t *testing.T, path string) *ConfirmDB {
	t.Helper()
	db, err := NewConfirmDB(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openPersistedStore builds the production wiring: an empty store attached to
// the database at path, absorbing whatever a prior "daemon run" persisted.
func openPersistedStore(t *testing.T, path string) (*PendingConfirmStore, *ConfirmDB) {
	t.Helper()
	db := newTestConfirmDB(t, path)
	store := NewPendingConfirmStore()
	require.NoError(t, store.WithPersistence(db, zerolog.Nop()))
	return store, db
}

func confirmFixture(id string) *PendingConfirmation {
	return &PendingConfirmation{
		ID:        id,
		Operation: "TransitionIssue",
		Tracker:   "shortcut",
		Key:       "200",
		Prompt:    "TransitionIssue 200?",
		ClientPID: 111,
		CreatedAt: time.Now().Add(-time.Minute),
	}
}

func TestConfirmDB_InsertLoadRoundTrip(t *testing.T) {
	db := newTestConfirmDB(t, testConfirmDBPath(t))

	unresolved := *confirmFixture("c-round")
	unresolved.State = ConfirmPending
	require.NoError(t, db.Insert(unresolved))

	resolved := *confirmFixture("c-resolved")
	resolved.State = ConfirmApproved
	resolved.ResolvedAt = time.Now()
	require.NoError(t, db.Insert(resolved))
	require.NoError(t, db.UpdateResolved(resolved))

	loaded, err := db.LoadAll()
	require.NoError(t, err)
	require.Len(t, loaded, 2)

	byID := map[string]PendingConfirmation{}
	for _, pc := range loaded {
		byID[pc.ID] = pc
	}
	got := byID["c-round"]
	require.Equal(t, unresolved.Operation, got.Operation)
	require.Equal(t, unresolved.Tracker, got.Tracker)
	require.Equal(t, unresolved.Key, got.Key)
	require.Equal(t, unresolved.Prompt, got.Prompt)
	require.Equal(t, unresolved.ClientPID, got.ClientPID)
	require.Equal(t, ConfirmPending, got.State)
	// Second-granularity truncation is acceptable; the zero/non-zero
	// distinction is what confirm-status relies on.
	require.True(t, got.ResolvedAt.IsZero(), "unresolved entry must load with zero ResolvedAt")
	require.WithinDuration(t, unresolved.CreatedAt, got.CreatedAt, time.Second)

	res := byID["c-resolved"]
	require.Equal(t, ConfirmApproved, res.State)
	require.False(t, res.ResolvedAt.IsZero(), "resolved entry must round-trip ResolvedAt")
	require.WithinDuration(t, resolved.ResolvedAt, res.ResolvedAt, time.Second)
}

func TestConfirmDB_InsertIgnoresDuplicateID(t *testing.T) {
	db := newTestConfirmDB(t, testConfirmDBPath(t))

	first := *confirmFixture("c-dup")
	first.State = ConfirmApproved
	first.ResolvedAt = time.Now()
	require.NoError(t, db.Insert(first))
	require.NoError(t, db.UpdateResolved(first))

	second := *confirmFixture("c-dup")
	second.State = ConfirmPending
	require.NoError(t, db.Insert(second))

	loaded, err := db.LoadAll()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, ConfirmApproved, loaded[0].State, "duplicate insert must not reset a decision")
}

func TestConfirmDB_Delete(t *testing.T) {
	db := newTestConfirmDB(t, testConfirmDBPath(t))
	require.NoError(t, db.Insert(*confirmFixture("c-del")))
	require.NoError(t, db.Delete("c-del"))

	loaded, err := db.LoadAll()
	require.NoError(t, err)
	require.Empty(t, loaded)
}

func TestConfirmDB_DeleteOlderThan(t *testing.T) {
	db := newTestConfirmDB(t, testConfirmDBPath(t))

	stale := *confirmFixture("c-stale")
	stale.CreatedAt = time.Now().Add(-48 * time.Hour)
	require.NoError(t, db.Insert(stale))
	require.NoError(t, db.Insert(*confirmFixture("c-fresh")))

	require.NoError(t, db.DeleteOlderThan(time.Now().Add(-ConfirmRetention)))

	loaded, err := db.LoadAll()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, "c-fresh", loaded[0].ID)
}

// The observed bug: a prompt created before a daemon restart must still be
// offered for approval afterwards.
func TestPendingConfirmStore_PendingSurvivesRestart(t *testing.T) {
	path := testConfirmDBPath(t)

	store, db := openPersistedStore(t, path)
	store.Submit(confirmFixture("c-live"))
	require.NoError(t, db.Close())

	reopened, _ := openPersistedStore(t, path)
	got, ok := reopened.Get("c-live")
	require.True(t, ok, "pending prompt must survive a restart")
	require.Equal(t, ConfirmPending, got.State)

	snap := reopened.Snapshot()
	require.Len(t, snap, 1)
	require.Equal(t, "c-live", snap[0].ID)
}

// A grant approved before a restart must stay redeemable after it — and stay
// consumed across yet another restart once redeemed.
func TestPendingConfirmStore_ApprovedGrantRedeemsAfterRestart(t *testing.T) {
	path := testConfirmDBPath(t)

	store, db := openPersistedStore(t, path)
	store.Submit(confirmFixture("c-grant"))
	_, err := store.Resolve("c-grant", true, 222)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	reopened, db2 := openPersistedStore(t, path)
	pc, ok := reopened.ConsumeApprovedFor("TransitionIssue", "shortcut", "200")
	require.True(t, ok, "approved grant must survive a restart")
	require.Equal(t, "c-grant", pc.ID)

	_, ok = reopened.ConsumeApprovedFor("TransitionIssue", "shortcut", "200")
	require.False(t, ok, "a grant redeems exactly once")
	require.NoError(t, db2.Close())

	again, _ := openPersistedStore(t, path)
	_, ok = again.ConsumeApprovedFor("TransitionIssue", "shortcut", "200")
	require.False(t, ok, "a redeemed grant must not resurrect on the next restart")
	_, ok = again.Get("c-grant")
	require.False(t, ok)
}

func TestPendingConfirmStore_ConsumeByIDAfterRestart(t *testing.T) {
	path := testConfirmDBPath(t)

	store, db := openPersistedStore(t, path)
	store.Submit(confirmFixture("c-exact"))
	_, err := store.Resolve("c-exact", true, 222)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	reopened, _ := openPersistedStore(t, path)
	_, ok := reopened.Consume("c-exact", "DeleteIssue", "shortcut", "200")
	require.False(t, ok, "a grant must not authorize a different operation")

	pc, ok := reopened.Consume("c-exact", "TransitionIssue", "shortcut", "200")
	require.True(t, ok)
	require.Equal(t, ConfirmApproved, pc.State)
}

func TestPendingConfirmStore_DeniedSurvivesRestart(t *testing.T) {
	path := testConfirmDBPath(t)

	store, db := openPersistedStore(t, path)
	store.Submit(confirmFixture("c-no"))
	_, err := store.Resolve("c-no", false, 222)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	reopened, _ := openPersistedStore(t, path)
	pc, ok := reopened.FindDenied("TransitionIssue", "shortcut", "200")
	require.True(t, ok, "a denial is final for the retention window, restarts included")
	require.Equal(t, "c-no", pc.ID)
	require.False(t, pc.ResolvedAt.IsZero())
}

// Cleanup must sweep both layers so a swept ID reads state "unknown"
// (= expired) even after a restart.
func TestPendingConfirmStore_CleanupPrunesBothLayers(t *testing.T) {
	path := testConfirmDBPath(t)

	store, db := openPersistedStore(t, path)
	old := confirmFixture("c-old")
	old.CreatedAt = time.Now().Add(-48 * time.Hour)
	store.Submit(old)
	store.Cleanup(ConfirmRetention)
	_, ok := store.Get("c-old")
	require.False(t, ok)
	require.NoError(t, db.Close())

	reopened, _ := openPersistedStore(t, path)
	_, ok = reopened.Get("c-old")
	require.False(t, ok, "a swept entry must not resurrect on restart")
}

// WithPersistence prunes stale rows before loading, so an expired entry from
// a long-stopped daemon is neither offered nor kept.
func TestPendingConfirmStore_StartupPruneDropsStale(t *testing.T) {
	path := testConfirmDBPath(t)

	db := newTestConfirmDB(t, path)
	stale := *confirmFixture("c-ancient")
	stale.State = ConfirmPending
	stale.CreatedAt = time.Now().Add(-2 * ConfirmRetention)
	require.NoError(t, db.Insert(stale))
	require.NoError(t, db.Close())

	store, db2 := openPersistedStore(t, path)
	require.Equal(t, 0, store.Len(), "stale rows must not load")

	loaded, err := db2.LoadAll()
	require.NoError(t, err)
	require.Empty(t, loaded, "startup prune must delete stale rows")
}

// A client retrying its pre-restart ConfirmID must reattach to the persisted
// entry, not reset an already-made decision to pending.
func TestPendingConfirmStore_ResubmitAfterRestartPreservesDecision(t *testing.T) {
	path := testConfirmDBPath(t)

	store, db := openPersistedStore(t, path)
	store.Submit(confirmFixture("c-retry"))
	_, err := store.Resolve("c-retry", true, 222)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	reopened, _ := openPersistedStore(t, path)
	reopened.Submit(confirmFixture("c-retry"))

	got, ok := reopened.Get("c-retry")
	require.True(t, ok)
	require.Equal(t, ConfirmApproved, got.State, "re-submit must not reset a persisted decision")
}

// failingPersistence simulates a broken disk: every write fails. deletes
// records Delete attempts so the Cleanup retry path can be observed after
// the failure mode is switched off.
type failingPersistence struct {
	failing bool
	deletes []string
}

func (f *failingPersistence) err(op string) error {
	if f.failing {
		return errTestPersistence(op)
	}
	return nil
}

func (f *failingPersistence) Insert(PendingConfirmation) error         { return f.err("insert") }
func (f *failingPersistence) UpdateResolved(PendingConfirmation) error { return f.err("update") }
func (f *failingPersistence) Delete(id string) error {
	if f.failing {
		return errTestPersistence("delete")
	}
	f.deletes = append(f.deletes, id)
	return nil
}
func (f *failingPersistence) DeleteOlderThan(time.Time) error      { return f.err("prune") }
func (f *failingPersistence) LoadAll() ([]PendingConfirmation, error) { return nil, f.err("load") }

type errTestPersistence string

func (e errTestPersistence) Error() string { return "test persistence failure: " + string(e) }

// A broken sink must degrade to exactly today's memory-only behavior, and a
// failed grant-consumption delete (the one dangerous direction) must be
// retried by the next Cleanup tick once the sink recovers.
func TestPendingConfirmStore_FailingSinkDegradesToMemory(t *testing.T) {
	sink := &failingPersistence{}
	store := NewPendingConfirmStore()
	require.NoError(t, store.WithPersistence(sink, zerolog.Nop()))
	sink.failing = true

	store.Submit(confirmFixture("c-mem"))
	_, err := store.Resolve("c-mem", true, 222)
	require.NoError(t, err)
	pc, ok := store.Consume("c-mem", "TransitionIssue", "shortcut", "200")
	require.True(t, ok, "memory semantics must not depend on the sink")
	require.Equal(t, ConfirmApproved, pc.State)

	sink.failing = false
	store.Cleanup(ConfirmRetention)
	require.Contains(t, sink.deletes, "c-mem", "Cleanup must retry the failed grant-consumption delete")
}
