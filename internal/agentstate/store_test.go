package agentstate

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedClock returns a clock whose value the test controls, so lease expiry is
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

	written, err := s.Set(ctx, "", "sc-1200", "triage.evidence", "nil deref in board_state", "",
		Meta{Agent: "alpha", RunID: "run-1"})
	require.NoError(t, err)
	require.Equal(t, "SC-1200", written.Scope, "scope is normalised to upper case")
	require.Equal(t, FormatText, written.Format)

	got, err := s.Get(ctx, "", "SC-1200", "triage.evidence")
	require.NoError(t, err)
	require.Equal(t, "nil deref in board_state", got.Value)
	require.Equal(t, "alpha", got.Agent)
	require.Equal(t, "run-1", got.RunID)
	require.Equal(t, written.UpdatedAt.UTC(), got.UpdatedAt.UTC())
}

// Two projects that happen to share a ticket key must never share state: a run
// in one project must not read, overwrite, or count the other's working
// memory (SC-2326).
func TestSet_ProjectsAreIsolated(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "alpha", "SC-1", "stage.fix", "A", "", Meta{Agent: "a"})
	require.NoError(t, err)
	_, err = s.Set(ctx, "beta", "SC-1", "stage.fix", "B", "", Meta{Agent: "b"})
	require.NoError(t, err)

	got, err := s.Get(ctx, "alpha", "SC-1", "stage.fix")
	require.NoError(t, err)
	require.Equal(t, "A", got.Value)

	got, err = s.Get(ctx, "beta", "SC-1", "stage.fix")
	require.NoError(t, err)
	require.Equal(t, "B", got.Value)
}

// A retry budget in one project must not be spent by, or visible to, a run of
// the same ticket key in another project.
func TestIncr_ProjectBudgetsAreIndependent(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Incr(ctx, "alpha", "SC-1", "budget.fix.attempts", 1, Meta{})
	require.NoError(t, err)
	total, err := s.Incr(ctx, "alpha", "SC-1", "budget.fix.attempts", 1, Meta{})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)

	betaTotal, err := s.Incr(ctx, "beta", "SC-1", "budget.fix.attempts", 1, Meta{})
	require.NoError(t, err)
	require.Equal(t, int64(1), betaTotal)
}

// A live lease in one project must not block a lease of the identical
// scope/stage in another, and a takeover must never hand a successor another
// project's inherited keys.
func TestLease_ProjectsDoNotBlockOrInherit(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "alpha", "SC-1", "triage.evidence", "x", "", Meta{Agent: "a"})
	require.NoError(t, err)

	res, err := s.Lease(ctx, LeaseRequest{Project: "alpha", Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "a"}})
	require.NoError(t, err)
	require.True(t, res.Granted)

	res, err = s.Lease(ctx, LeaseRequest{Project: "beta", Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "b"}})
	require.NoError(t, err)
	require.True(t, res.Granted, "a live lease in alpha must not block beta")
	require.Nil(t, res.Displaced)
	require.Empty(t, res.InheritedKeys)

	leases, err := s.Leases(ctx, "beta", "SC-1")
	require.NoError(t, err)
	require.Len(t, leases, 1)
	require.Equal(t, "b", leases[0].Agent)
}

// LiveLeases must see every scope of a project, exclude a released lease,
// exclude an expired one, and never cross into another project — exactly the
// signal the desktop close flow relies on before stopping a busy daemon
// (SC-3015).
func TestLiveLeases_ReturnsOnlyLiveAcrossScopesWithinOneProject(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	// Live: proj/SC-1/fix.
	_, err := s.Lease(ctx, LeaseRequest{Project: "proj", Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "a"}, TTL: 15 * time.Minute})
	require.NoError(t, err)

	// Released: proj/SC-2/plan — must not count as live.
	_, err = s.Lease(ctx, LeaseRequest{Project: "proj", Scope: "SC-2", Stage: "plan", Meta: Meta{Agent: "b"}, TTL: 15 * time.Minute})
	require.NoError(t, err)
	released, err := s.Release(ctx, "proj", "SC-2", "plan", "b")
	require.NoError(t, err)
	require.True(t, released)

	// Expired: proj/SC-3/triage — heartbeated past its own TTL.
	_, err = s.Lease(ctx, LeaseRequest{Project: "proj", Scope: "SC-3", Stage: "triage", Meta: Meta{Agent: "c"}, TTL: time.Minute})
	require.NoError(t, err)
	*clock = clock.Add(2 * time.Minute)

	// Different project — must never surface here even though it's live.
	_, err = s.Lease(ctx, LeaseRequest{Project: "other", Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "d"}, TTL: 15 * time.Minute})
	require.NoError(t, err)

	live, err := s.LiveLeases(ctx, "proj")
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, "SC-1", live[0].Scope)
	assert.Equal(t, "fix", live[0].Stage)
}

func TestLiveLeases_NoLeasesReturnsEmptyNotNilError(t *testing.T) {
	s, _ := newTestStore(t)
	live, err := s.LiveLeases(context.Background(), "proj")
	require.NoError(t, err)
	require.Empty(t, live)
}

// A pre-project database (created before SC-2326) must migrate its rows to the
// default project "" rather than lose them or fail to open — no reconfiguration,
// no visible migration step for existing single-project installs.
func TestOpen_MigratesLegacyRowsToDefaultProject(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.db"

	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TABLE agent_state (
		scope      TEXT NOT NULL,
		name       TEXT NOT NULL,
		value      TEXT NOT NULL,
		format     TEXT NOT NULL DEFAULT 'text',
		agent      TEXT NOT NULL DEFAULT '',
		run_id     TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY (scope, name)
	)`)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TABLE agent_leases (
		scope        TEXT NOT NULL,
		stage        TEXT NOT NULL,
		agent        TEXT NOT NULL,
		run_id       TEXT NOT NULL DEFAULT '',
		ttl_seconds  INTEGER NOT NULL DEFAULT 0,
		leased_at   TEXT NOT NULL,
		heartbeat_at TEXT NOT NULL,
		released_at  TEXT,
		PRIMARY KEY (scope, stage)
	)`)
	require.NoError(t, err)

	stamp := time.Now().UTC().Format(TimeFormat)
	_, err = raw.Exec(`INSERT INTO agent_state (scope, name, value, format, agent, run_id, updated_at)
		VALUES ('SC-1', 'fix.evidence', 'legacy value', 'text', 'alpha', 'run-1', ?)`, stamp)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO agent_leases (scope, stage, agent, run_id, ttl_seconds, leased_at, heartbeat_at, released_at)
		VALUES ('SC-1', 'fix', 'alpha', 'run-1', 900, ?, ?, NULL)`, stamp, stamp)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	s, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	got, err := s.Get(context.Background(), "", "SC-1", "fix.evidence")
	require.NoError(t, err)
	require.Equal(t, "legacy value", got.Value, "legacy rows are reachable under the default project")

	leases, err := s.Leases(context.Background(), "", "SC-1")
	require.NoError(t, err)
	require.Len(t, leases, 1)
	require.Equal(t, "alpha", leases[0].Agent)
}

// A lower-case scope must reach the same row as the upper-case one, so an agent
// that echoes the user's casing does not start a second, invisible scope.
func TestGet_ScopeIsCaseInsensitive(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "", "SC-1200", "stage.fix", "running", "", Meta{})
	require.NoError(t, err)

	got, err := s.Get(ctx, "", "sc-1200", "stage.fix")
	require.NoError(t, err)
	require.Equal(t, "running", got.Value)
}

func TestGet_MissingReturnsErrNotFound(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Get(context.Background(), "", "SC-1", "nope")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSet_OverwritesInPlace(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "", "SC-1", "stage.fix", "first", "", Meta{Agent: "alpha"})
	require.NoError(t, err)

	*clock = clock.Add(time.Minute)
	_, err = s.Set(ctx, "", "SC-1", "stage.fix", "second", "", Meta{Agent: "beta"})
	require.NoError(t, err)

	got, err := s.Get(ctx, "", "SC-1", "stage.fix")
	require.NoError(t, err)
	require.Equal(t, "second", got.Value)
	require.Equal(t, "beta", got.Agent, "provenance follows the latest writer")

	all, err := s.List(ctx, "", "SC-1", "")
	require.NoError(t, err)
	require.Len(t, all, 1, "overwrite must not create a second row")
}

func TestSet_JSONFormatValidated(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "", "SC-1", "stage.triage", `{"status":"confirmed"}`, FormatJSON, Meta{})
	require.NoError(t, err)

	_, err = s.Set(ctx, "", "SC-1", "stage.broken", `{"status":`, FormatJSON, Meta{})
	require.Error(t, err, "a half-written JSON blob must be rejected at write time")
	require.Contains(t, err.Error(), "not valid JSON")
}

func TestSet_UnknownFormatRejected(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Set(context.Background(), "", "SC-1", "k", "v", "yaml", Meta{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown format")
}

func TestSet_RejectsEmptyScopeAndBadName(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "", "   ", "k", "v", "", Meta{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "scope must not be empty")

	for _, bad := range []string{"", ".leading", "has space", "wild*card", "pct%"} {
		_, err := s.Set(ctx, "", "SC-1", bad, "v", "", Meta{})
		require.Error(t, err, "name %q must be rejected", bad)
	}
}

func TestSet_RejectsOversizeValue(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Set(context.Background(), "", "SC-1", "big", strings.Repeat("x", MaxValueBytes+1), "", Meta{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "size cap")
}

func TestList_PrefixTreatsUnderscoreLiterally(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"budget_fix", "budgetXfix", "budget.plan", "triage.evidence"} {
		_, err := s.Set(ctx, "", "SC-1", name, "v", "", Meta{})
		require.NoError(t, err)
	}

	// "_" is a LIKE wildcard; unescaped it would also match "budgetXfix".
	got, err := s.List(ctx, "", "SC-1", "budget_")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "budget_fix", got[0].Name)

	all, err := s.List(ctx, "", "SC-1", "")
	require.NoError(t, err)
	require.Len(t, all, 4)
	require.Equal(t, "budget.plan", all[0].Name, "results are ordered by name")
}

func TestList_UnknownScopeIsEmptyNotError(t *testing.T) {
	s, _ := newTestStore(t)

	got, err := s.List(context.Background(), "", "SC-NOPE", "")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestDelete_AndDeleteScope(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "", "SC-1", "a", "1", "", Meta{})
	require.NoError(t, err)
	_, err = s.Set(ctx, "", "SC-1", "b", "2", "", Meta{})
	require.NoError(t, err)
	_, err = s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	removed, err := s.Delete(ctx, "", "SC-1", "a")
	require.NoError(t, err)
	require.True(t, removed)

	removed, err = s.Delete(ctx, "", "SC-1", "a")
	require.NoError(t, err)
	require.False(t, removed, "deleting a missing entry reports false, not an error")

	n, err := s.DeleteScope(ctx, "", "SC-1")
	require.NoError(t, err)
	require.Equal(t, 1, n)

	leases, err := s.Leases(ctx, "", "SC-1")
	require.NoError(t, err)
	require.Empty(t, leases, "dropping a scope must drop its leases too")
}

// Clearing a namespace is how a fresh run drops the previous run's retry
// budgets without disturbing the rest of the ticket's state.
func TestDeletePrefix_ClearsOnlyTheNamespace(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	for _, n := range []string{"budget.fix.attempts", "budget.fix.flakes", "budget.review.attempts", "fix.evidence", "budgetary"} {
		_, err := s.Set(ctx, "", "SC-1", n, "1", "", Meta{})
		require.NoError(t, err)
	}

	n, err := s.DeletePrefix(ctx, "", "SC-1", "budget.")
	require.NoError(t, err)
	require.Equal(t, 3, n)

	remaining, err := s.List(ctx, "", "SC-1", "")
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
	_, err := s.Set(ctx, "", "SC-1", "keep", "1", "", Meta{})
	require.NoError(t, err)

	_, err = s.DeletePrefix(ctx, "", "SC-1", "  ")
	require.Error(t, err)

	remaining, err := s.List(ctx, "", "SC-1", "")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
}

func TestDeletePrefix_UnderscoreIsLiteral(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	_, err := s.Set(ctx, "", "SC-1", "budget_fix", "1", "", Meta{})
	require.NoError(t, err)
	_, err = s.Set(ctx, "", "SC-1", "budgetXfix", "1", "", Meta{})
	require.NoError(t, err)

	n, err := s.DeletePrefix(ctx, "", "SC-1", "budget_")
	require.NoError(t, err)
	require.Equal(t, 1, n, "_ must not act as a LIKE wildcard")
}

func TestIncr_CountsFromZeroAndAccumulates(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	n, err := s.Incr(ctx, "", "SC-1", "budget.fix.attempts", 1, Meta{Agent: "alpha"})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	n, err = s.Incr(ctx, "", "SC-1", "budget.fix.attempts", 2, Meta{Agent: "alpha"})
	require.NoError(t, err)
	require.Equal(t, int64(3), n)

	got, err := s.Get(ctx, "", "SC-1", "budget.fix.attempts")
	require.NoError(t, err)
	require.Equal(t, "3", got.Value)
	require.Equal(t, FormatText, got.Format)
}

// A counter must never silently reset a value that is not a number — that would
// quietly hand a stage a fresh retry budget.
func TestIncr_RefusesNonNumericValue(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "", "SC-1", "notes", "some prose", "", Meta{})
	require.NoError(t, err)

	_, err = s.Incr(ctx, "", "SC-1", "notes", 1, Meta{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a counter")

	got, err := s.Get(ctx, "", "SC-1", "notes")
	require.NoError(t, err)
	require.Equal(t, "some prose", got.Value, "the original value survives a refused increment")
}

func TestPrune_DropsOnlyEntriesOlderThanCutoff(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(ctx, "", "SC-1", "old", "v", "", Meta{})
	require.NoError(t, err)

	*clock = clock.Add(10 * 24 * time.Hour)
	_, err = s.Set(ctx, "", "SC-1", "fresh", "v", "", Meta{})
	require.NoError(t, err)

	cutoff := clock.Add(-time.Hour)
	n, err := s.Prune(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	remaining, err := s.List(ctx, "", "SC-1", "")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, "fresh", remaining[0].Name)
}

func TestClaim_FreshStageIsGranted(t *testing.T) {
	s, _ := newTestStore(t)

	res, err := s.Lease(context.Background(), LeaseRequest{
		Scope: "sc-1200", Stage: "fix", Meta: Meta{Agent: "alpha", RunID: "run-1"},
	})
	require.NoError(t, err)
	require.True(t, res.Granted)
	require.Equal(t, "SC-1200", res.Lease.Scope)
	require.Equal(t, "alpha", res.Lease.Agent)
	require.Nil(t, res.Displaced)
}

func TestClaim_RefusedWhileAnotherAgentHoldsItLive(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	*clock = clock.Add(time.Minute)
	res, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}})
	require.NoError(t, err, "a refused lease is a result, not an error")
	require.False(t, res.Granted)
	require.Equal(t, "alpha", res.Lease.Agent, "the refusal names the holder")
}

// The takeover path: once the holder stops heartbeating past the TTL, a fresh
// agent inherits the stage and is told what the dead one left behind.
func TestClaim_StaleClaimIsTakenOverWithInheritance(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Lease(ctx, LeaseRequest{
		Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}, TTL: 5 * time.Minute,
	})
	require.NoError(t, err)
	_, err = s.Set(ctx, "", "SC-1", "fix.evidence", "root cause found", "", Meta{Agent: "alpha"})
	require.NoError(t, err)
	_, err = s.Set(ctx, "", "SC-1", "unrelated", "x", "", Meta{Agent: "gamma"})
	require.NoError(t, err)

	*clock = clock.Add(6 * time.Minute)
	res, err := s.Lease(ctx, LeaseRequest{
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
// heartbeat cadence: a short-lived stage leased with --ttl 2s stayed
// un-reclaimable for the challenger's default 15 minutes.
func TestClaim_StalenessUsesTheHoldersOwnTTL(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Lease(ctx, LeaseRequest{
		Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}, TTL: 2 * time.Second,
	})
	require.NoError(t, err)

	*clock = clock.Add(10 * time.Second)

	// beta asks with the default (long) TTL; alpha's own 2s TTL has lapsed.
	res, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}})
	require.NoError(t, err)
	require.True(t, res.Granted, "the holder's short TTL decides, not the challenger's long one")
	require.NotNil(t, res.Displaced)
	require.Equal(t, "alpha", res.Displaced.Agent)
	require.Equal(t, DefaultLeaseTTL, res.Lease.TTL, "beta's own lease carries beta's TTL")
}

// The mirror case: a holder with a long TTL is not stolen by a challenger that
// happens to pass a short one.
func TestClaim_ShortChallengerTTLCannotStealALiveClaim(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Lease(ctx, LeaseRequest{
		Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}, TTL: time.Hour,
	})
	require.NoError(t, err)

	*clock = clock.Add(10 * time.Minute)

	res, err := s.Lease(ctx, LeaseRequest{
		Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}, TTL: time.Second,
	})
	require.NoError(t, err)
	require.False(t, res.Granted)
	require.Equal(t, "alpha", res.Lease.Agent)
}

func TestClaim_TakeoverOverridesALiveClaim(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	res, err := s.Lease(ctx, LeaseRequest{
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

	first, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	*clock = clock.Add(3 * time.Minute)
	second, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)
	require.True(t, second.Granted)
	require.Nil(t, second.Displaced, "refreshing your own lease displaces nobody")
	require.Equal(t, first.Lease.LeasedAt.UTC(), second.Lease.LeasedAt.UTC())
	require.True(t, second.Lease.HeartbeatAt.After(first.Lease.HeartbeatAt))
}

func TestClaim_RequiresAgentName(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Lease(context.Background(), LeaseRequest{Scope: "SC-1", Stage: "fix"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires an agent name")
}

func TestClaim_RejectsBadStageName(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Lease(context.Background(), LeaseRequest{
		Scope: "SC-1", Stage: "not a stage", Meta: Meta{Agent: "alpha"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "stage must be a simple name")
}

func TestRelease_FreesTheStageForAnotherAgent(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	released, err := s.Release(ctx, "", "SC-1", "fix", "alpha")
	require.NoError(t, err)
	require.True(t, released)

	res, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}})
	require.NoError(t, err)
	require.True(t, res.Granted)
	require.Nil(t, res.Displaced, "a released holder handed off; it was not displaced")
}

// A stale process must not be able to release the lease of the agent that took
// over from it.
func TestRelease_OnlyAffectsTheNamedAgentsClaim(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}})
	require.NoError(t, err)

	released, err := s.Release(ctx, "", "SC-1", "fix", "alpha")
	require.NoError(t, err)
	require.False(t, released)

	leases, err := s.Leases(ctx, "", "SC-1")
	require.NoError(t, err)
	require.Len(t, leases, 1)
	require.Nil(t, leases[0].ReleasedAt)
}

func TestRelease_WithoutAgentReleasesAnyHolder(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	released, err := s.Release(ctx, "", "SC-1", "fix", "")
	require.NoError(t, err)
	require.True(t, released)

	released, err = s.Release(ctx, "", "SC-1", "fix", "")
	require.NoError(t, err)
	require.False(t, released, "releasing twice is a no-op")
}

func TestClaims_ListsReleasedAndLiveClaims(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "triage", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)
	_, err = s.Release(ctx, "", "SC-1", "triage", "alpha")
	require.NoError(t, err)

	*clock = clock.Add(time.Minute)
	_, err = s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "beta"}})
	require.NoError(t, err)

	leases, err := s.Leases(ctx, "", "SC-1")
	require.NoError(t, err)
	require.Len(t, leases, 2)
	require.Equal(t, "fix", leases[0].Stage, "newest heartbeat first")
	require.Nil(t, leases[0].ReleasedAt)

	require.Equal(t, "triage", leases[1].Stage)
	require.NotNil(t, leases[1].ReleasedAt)
}

func TestClaims_EmptyScopeIsAnError(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.Leases(context.Background(), "", "")
	require.Error(t, err)
}

func TestPrune_DropsStaleClaims(t *testing.T) {
	s, clock := newTestStore(t)
	ctx := context.Background()

	_, err := s.Lease(ctx, LeaseRequest{Scope: "SC-1", Stage: "fix", Meta: Meta{Agent: "alpha"}})
	require.NoError(t, err)

	*clock = clock.Add(30 * 24 * time.Hour)
	_, err = s.Prune(ctx, clock.Add(-DefaultRetention))
	require.NoError(t, err)

	leases, err := s.Leases(ctx, "", "SC-1")
	require.NoError(t, err)
	require.Empty(t, leases)
}

func TestOpen_CreatesDatabaseFileAndReopens(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nested/state.db"

	s, err := Open(path)
	require.NoError(t, err)
	_, err = s.Set(context.Background(), "", "SC-1", "k", "v", "", Meta{Agent: "alpha"})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	reopened, err := Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()

	got, err := reopened.Get(context.Background(), "", "SC-1", "k")
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
	_, err := store.Set(context.Background(), "", "SC-1", "k", "v", "", Meta{})
	require.NoError(t, err)

	_, err = store.Get(context.Background(), "", "SC-1", "missing")
	require.True(t, errors.Is(err, ErrNotFound))
}
