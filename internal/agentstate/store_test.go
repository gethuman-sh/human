package agentstate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fixedClock returns a clock whose value the test controls, so claim expiry is
// exercised by moving time rather than sleeping.
func fixedClock(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

func newTestStore(t *testing.T) (*SQLiteStore, *time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	clock := &now
	s, err := Open(":memory:", WithClock(fixedClock(clock)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s, clock
}

func TestSet_GetRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	written, err := s.Set(ctx, "sc-1200", "triage.evidence", "nil deref in board_state", "",
		Meta{Agent: "alpha", RunID: "run-1"})
	require.NoError(t, err)
	require.Equal(t, "SC-1200", written.Scope, "scope is normalised to upper case")
	require.Equal(t, FormatText, written.Format)

	got, err := s.Get(ctx, "SC-1200", "triage.evidence")
	require.NoError(t, err)
	require.Equal(t, "nil deref in board_state", got.Value)
	require.Equal(t, "alpha", got.Agent)
	require.Equal(t, "run-1", got.RunID)
	require.Equal(t, written.UpdatedAt.UTC(), got.UpdatedAt.UTC())
}

// A lower-case scope must reach the same row as the upper-case one, so an agent
// that echoes the user's casing does not start a second, invisible scope.
func TestGet_ScopeIsCaseInsensitive(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "SC-1200", "stage.fix", "running", "", Meta{})
	require.NoError(t, err)

	got, err := s.Get(ctx, "sc-1200", "stage.fix")
	require.NoError(t, err)
	require.Equal(t, "running", got.Value)
}

func TestGet_MissingReturnsErrNotFound(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Get(context.Background(), "SC-1", "nope")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSet_OverwritesInPlace(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "SC-1", "stage.fix", "first", "", Meta{Agent: "alpha"})
	require.NoError(t, err)

	*clock = clock.Add(time.Minute)
	_, err = s.Set(ctx, "SC-1", "stage.fix", "second", "", Meta{Agent: "beta"})
	require.NoError(t, err)

	got, err := s.Get(ctx, "SC-1", "stage.fix")
	require.NoError(t, err)
	require.Equal(t, "second", got.Value)
	require.Equal(t, "beta", got.Agent, "provenance follows the latest writer")

	all, err := s.List(ctx, "SC-1", "")
	require.NoError(t, err)
	require.Len(t, all, 1, "overwrite must not create a second row")
}

func TestSet_JSONFormatValidated(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "SC-1", "stage.triage", `{"status":"confirmed"}`, FormatJSON, Meta{})
	require.NoError(t, err)

	_, err = s.Set(ctx, "SC-1", "stage.broken", `{"status":`, FormatJSON, Meta{})
	require.Error(t, err, "a half-written JSON blob must be rejected at write time")
	require.Contains(t, err.Error(), "not valid JSON")
}

func TestSet_UnknownFormatRejected(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Set(context.Background(), "SC-1", "k", "v", "yaml", Meta{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown format")
}

func TestSet_RejectsEmptyScopeAndBadName(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "   ", "k", "v", "", Meta{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "scope must not be empty")

	for _, bad := range []string{"", ".leading", "has space", "wild*card", "pct%"} {
		_, err := s.Set(ctx, "SC-1", bad, "v", "", Meta{})
		require.Error(t, err, "name %q must be rejected", bad)
	}
}

func TestSet_RejectsOversizeValue(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Set(context.Background(), "SC-1", "big", strings.Repeat("x", MaxValueBytes+1), "", Meta{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "size cap")
}

func TestList_PrefixTreatsUnderscoreLiterally(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"budget_fix", "budgetXfix", "budget.plan", "triage.evidence"} {
		_, err := s.Set(ctx, "SC-1", name, "v", "", Meta{})
		require.NoError(t, err)
	}

	// "_" is a LIKE wildcard; unescaped it would also match "budgetXfix".
	got, err := s.List(ctx, "SC-1", "budget_")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "budget_fix", got[0].Name)

	all, err := s.List(ctx, "SC-1", "")
	require.NoError(t, err)
	require.Len(t, all, 4)
	require.Equal(t, "budget.plan", all[0].Name, "results are ordered by name")
}

func TestList_UnknownScopeIsEmptyNotError(t *testing.T) {
	s, _ := newTestStore(t)

	got, err := s.List(context.Background(), "SC-NOPE", "")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestDelete_AndDeleteScope(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "SC-1", "a", "1", "", Meta{})
	require.NoError(t, err)
	_, err = s.Set(ctx, "SC-1", "b", "2", "", Meta{})
	require.NoError(t, err)
	_, err = s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	removed, err := s.Delete(ctx, "SC-1", "a")
	require.NoError(t, err)
	require.True(t, removed)

	removed, err = s.Delete(ctx, "SC-1", "a")
	require.NoError(t, err)
	require.False(t, removed, "deleting a missing entry reports false, not an error")

	n, err := s.DeleteScope(ctx, "SC-1")
	require.NoError(t, err)
	require.Equal(t, 1, n)

	claims, err := s.Claims(ctx, "SC-1")
	require.NoError(t, err)
	require.Empty(t, claims, "dropping a scope must drop its claims too")
}

// Clearing a namespace is how a fresh run drops the previous run's retry
// budgets without disturbing the rest of the ticket's state.
func TestDeletePrefix_ClearsOnlyTheNamespace(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	for _, n := range []string{"budget.fix.attempts", "budget.fix.flakes", "budget.review.attempts", "fix.evidence", "budgetary"} {
		_, err := s.Set(ctx, "SC-1", n, "1", "", Meta{})
		require.NoError(t, err)
	}

	n, err := s.DeletePrefix(ctx, "SC-1", "budget.")
	require.NoError(t, err)
	require.Equal(t, 3, n)

	remaining, err := s.List(ctx, "SC-1", "")
	require.NoError(t, err)
	names := []string{}
	for _, e := range remaining {
		names = append(names, e.Name)
	}
	require.ElementsMatch(t, []string{"fix.evidence", "budgetary"}, names,
		"only the dotted namespace goes; a name that merely starts with the same letters stays")
}

// An empty prefix must not quietly mean "everything" — a typo would wipe a
// ticket's whole working memory.
func TestDeletePrefix_RefusesEmptyPrefix(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	_, err := s.Set(ctx, "SC-1", "keep", "1", "", Meta{})
	require.NoError(t, err)

	_, err = s.DeletePrefix(ctx, "SC-1", "  ")
	require.Error(t, err)

	remaining, err := s.List(ctx, "SC-1", "")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
}

func TestDeletePrefix_UnderscoreIsLiteral(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	_, err := s.Set(ctx, "SC-1", "budget_fix", "1", "", Meta{})
	require.NoError(t, err)
	_, err = s.Set(ctx, "SC-1", "budgetXfix", "1", "", Meta{})
	require.NoError(t, err)

	n, err := s.DeletePrefix(ctx, "SC-1", "budget_")
	require.NoError(t, err)
	require.Equal(t, 1, n, "_ must not act as a LIKE wildcard")
}

func TestIncr_CountsFromZeroAndAccumulates(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	n, err := s.Incr(ctx, "SC-1", "budget.fix.attempts", 1, Meta{Agent: "alpha"})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	n, err = s.Incr(ctx, "SC-1", "budget.fix.attempts", 2, Meta{Agent: "alpha"})
	require.NoError(t, err)
	require.Equal(t, int64(3), n)

	got, err := s.Get(ctx, "SC-1", "budget.fix.attempts")
	require.NoError(t, err)
	require.Equal(t, "3", got.Value)
	require.Equal(t, FormatText, got.Format)
}

// A counter must never silently reset a value that is not a number — that would
// quietly hand a stage a fresh retry budget.
func TestIncr_RefusesNonNumericValue(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "SC-1", "notes", "some prose", "", Meta{})
	require.NoError(t, err)

	_, err = s.Incr(ctx, "SC-1", "notes", 1, Meta{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a counter")

	got, err := s.Get(ctx, "SC-1", "notes")
	require.NoError(t, err)
	require.Equal(t, "some prose", got.Value, "the original value survives a refused increment")
}

func TestPrune_DropsOnlyEntriesOlderThanCutoff(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "SC-1", "old", "v", "", Meta{})
	require.NoError(t, err)

	*clock = clock.Add(10 * 24 * time.Hour)
	_, err = s.Set(ctx, "SC-1", "fresh", "v", "", Meta{})
	require.NoError(t, err)

	cutoff := clock.Add(-time.Hour)
	n, err := s.Prune(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	remaining, err := s.List(ctx, "SC-1", "")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, "fresh", remaining[0].Name)
}

func TestClaim_FreshStageIsGranted(t *testing.T) {
	s, _ := newTestStore(t)

	res, err := s.Claim(context.Background(), ClaimRequest{
		Scope: "sc-1200", Stage: "fix", Meta: Meta{Agent: "alpha", RunID: "run-1"},
	})
	require.NoError(t, err)
	require.True(t, res.Granted)
	require.Equal(t, "SC-1200", res.Claim.Scope)
	require.Equal(t, "alpha", res.Claim.Agent)
	require.Nil(t, res.Displaced)
}

func TestClaim_RefusedWhileAnotherAgentHoldsItLive(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	*clock = clock.Add(time.Minute)
	res, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}})
	require.NoError(t, err, "a refused claim is a result, not an error")
	require.False(t, res.Granted)
	require.Equal(t, "alpha", res.Claim.Agent, "the refusal names the holder")
}

// The takeover path: once the holder stops heartbeating past the TTL, a fresh
// agent inherits the stage and is told what the dead one left behind.
func TestClaim_StaleClaimIsTakenOverWithInheritance(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, ClaimRequest{
		Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}, TTL: 5 * time.Minute,
	})
	require.NoError(t, err)
	_, err = s.Set(ctx, "SC-1", "fix.evidence", "root cause found", "", Meta{Agent: "alpha"})
	require.NoError(t, err)
	_, err = s.Set(ctx, "SC-1", "unrelated", "x", "", Meta{Agent: "gamma"})
	require.NoError(t, err)

	*clock = clock.Add(6 * time.Minute)
	res, err := s.Claim(ctx, ClaimRequest{
		Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}, TTL: 5 * time.Minute,
	})
	require.NoError(t, err)
	require.True(t, res.Granted)
	require.NotNil(t, res.Displaced)
	require.Equal(t, "alpha", res.Displaced.Agent)
	require.Equal(t, []string{"fix.evidence"}, res.InheritedKeys,
		"only the displaced agent's keys are reported as inherited")
}

// Staleness is judged by the TTL the holder declared, never by the
// challenger's. Otherwise a successor would have to guess its predecessor's
// heartbeat cadence: a short-lived stage claimed with --ttl 2s stayed
// un-reclaimable for the challenger's default 15 minutes.
func TestClaim_StalenessUsesTheHoldersOwnTTL(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, ClaimRequest{
		Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}, TTL: 2 * time.Second,
	})
	require.NoError(t, err)

	*clock = clock.Add(10 * time.Second)

	// beta asks with the default (long) TTL; alpha's own 2s TTL has lapsed.
	res, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}})
	require.NoError(t, err)
	require.True(t, res.Granted, "the holder's short TTL decides, not the challenger's long one")
	require.NotNil(t, res.Displaced)
	require.Equal(t, "alpha", res.Displaced.Agent)
	require.Equal(t, DefaultClaimTTL, res.Claim.TTL, "beta's own claim carries beta's TTL")
}

// The mirror case: a holder with a long TTL is not stolen by a challenger that
// happens to pass a short one.
func TestClaim_ShortChallengerTTLCannotStealALiveClaim(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, ClaimRequest{
		Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}, TTL: time.Hour,
	})
	require.NoError(t, err)

	*clock = clock.Add(10 * time.Minute)

	res, err := s.Claim(ctx, ClaimRequest{
		Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}, TTL: time.Second,
	})
	require.NoError(t, err)
	require.False(t, res.Granted)
	require.Equal(t, "alpha", res.Claim.Agent)
}

func TestClaim_TakeoverOverridesALiveClaim(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	res, err := s.Claim(ctx, ClaimRequest{
		Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}, Takeover: true,
	})
	require.NoError(t, err)
	require.True(t, res.Granted)
	require.NotNil(t, res.Displaced)
	require.Equal(t, "alpha", res.Displaced.Agent)
}

func TestClaim_SameAgentHeartbeatKeepsOriginalClaimTime(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	first, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	*clock = clock.Add(3 * time.Minute)
	second, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)
	require.True(t, second.Granted)
	require.Nil(t, second.Displaced, "refreshing your own claim displaces nobody")
	require.Equal(t, first.Claim.ClaimedAt.UTC(), second.Claim.ClaimedAt.UTC())
	require.True(t, second.Claim.HeartbeatAt.After(first.Claim.HeartbeatAt))
}

func TestClaim_RequiresAgentName(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Claim(context.Background(), ClaimRequest{Scope: "SC-1", Stage: "fix"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires an agent name")
}

func TestClaim_RejectsBadStageName(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Claim(context.Background(), ClaimRequest{
		Scope: "SC-1", Stage: "not a stage", Meta: Meta{Agent: "alpha"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "stage must be a simple name")
}

func TestRelease_FreesTheStageForAnotherAgent(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	released, err := s.Release(ctx, "SC-1", "fix", "alpha")
	require.NoError(t, err)
	require.True(t, released)

	res, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}})
	require.NoError(t, err)
	require.True(t, res.Granted)
	require.Nil(t, res.Displaced, "a released holder handed off; it was not displaced")
}

// A stale process must not be able to release the claim of the agent that took
// over from it.
func TestRelease_OnlyAffectsTheNamedAgentsClaim(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}})
	require.NoError(t, err)

	released, err := s.Release(ctx, "SC-1", "fix", "alpha")
	require.NoError(t, err)
	require.False(t, released)

	claims, err := s.Claims(ctx, "SC-1")
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Nil(t, claims[0].ReleasedAt)
}

func TestRelease_WithoutAgentReleasesAnyHolder(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	released, err := s.Release(ctx, "SC-1", "fix", "")
	require.NoError(t, err)
	require.True(t, released)

	released, err = s.Release(ctx, "SC-1", "fix", "")
	require.NoError(t, err)
	require.False(t, released, "releasing twice is a no-op")
}

func TestClaims_ListsReleasedAndLiveClaims(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "triage", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)
	_, err = s.Release(ctx, "SC-1", "triage", "alpha")
	require.NoError(t, err)

	*clock = clock.Add(time.Minute)
	_, err = s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}})
	require.NoError(t, err)

	claims, err := s.Claims(ctx, "SC-1")
	require.NoError(t, err)
	require.Len(t, claims, 2)
	require.Equal(t, "fix", claims[0].Stage, "newest heartbeat first")
	require.Nil(t, claims[0].ReleasedAt)

	require.Equal(t, "triage", claims[1].Stage)
	require.NotNil(t, claims[1].ReleasedAt)
}

func TestClaims_EmptyScopeIsAnError(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Claims(context.Background(), "")
	require.Error(t, err)
}

func TestPrune_DropsStaleClaims(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Claim(ctx, ClaimRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	*clock = clock.Add(30 * 24 * time.Hour)
	_, err = s.Prune(ctx, clock.Add(-DefaultRetention))
	require.NoError(t, err)

	claims, err := s.Claims(ctx, "SC-1")
	require.NoError(t, err)
	require.Empty(t, claims)
}

func TestOpen_CreatesDatabaseFileAndReopens(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nested/state.db"

	s, err := Open(path)
	require.NoError(t, err)
	_, err = s.Set(context.Background(), "SC-1", "k", "v", "", Meta{Agent: "alpha"})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	reopened, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()

	got, err := reopened.Get(context.Background(), "SC-1", "k")
	require.NoError(t, err)
	require.Equal(t, "v", got.Value, "state survives a restart — that is the point of persisting it")
}

func TestOpen_UnwritablePathFails(t *testing.T) {
	_, err := Open("/proc/definitely-not-writable/state.db")
	require.Error(t, err)
}

func TestDefaultDBPath_LandsInHumanDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	path := DefaultDBPath()
	require.True(t, strings.HasSuffix(path, "/.human/state.db"), "got %q", path)
}

// The store must be usable through the interface alone: the command layer
// depends on Store, never on the concrete type.
func TestSQLiteStore_SatisfiesStoreInterface(t *testing.T) {
	s, _ := newTestStore(t)

	var store Store = s
	_, err := store.Set(context.Background(), "SC-1", "k", "v", "", Meta{})
	require.NoError(t, err)

	_, err = store.Get(context.Background(), "SC-1", "missing")
	require.True(t, errors.Is(err, ErrNotFound))
}
