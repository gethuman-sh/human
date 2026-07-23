package cmdstate

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/env"
)

// fakeStore is an in-memory Store so command tests exercise flag handling and
// output without SQLite or the user's home directory.
type fakeStore struct {
	entries   map[string]agentstate.Entry
	leases    map[string]agentstate.Lease
	lastMeta  agentstate.Meta
	claimResp *agentstate.LeaseResult
	closed    bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		entries: map[string]agentstate.Entry{},
		leases:  map[string]agentstate.Lease{},
	}
}

func (f *fakeStore) key(scope, name string) string { return strings.ToUpper(scope) + "\x00" + name }

func (f *fakeStore) Set(_ context.Context, scope, name, value, format string, meta agentstate.Meta) (agentstate.Entry, error) {
	if format == agentstate.FormatJSON && !json.Valid([]byte(value)) {
		return agentstate.Entry{}, agentstate.ErrNotFound
	}
	if format == "" {
		format = agentstate.FormatText
	}
	f.lastMeta = meta
	e := agentstate.Entry{
		Scope: strings.ToUpper(scope), Name: name, Value: value, Format: format,
		Agent: meta.Agent, RunID: meta.RunID, UpdatedAt: time.Unix(0, 0).UTC(),
	}
	f.entries[f.key(scope, name)] = e
	return e, nil
}

func (f *fakeStore) Get(_ context.Context, scope, name string) (agentstate.Entry, error) {
	e, ok := f.entries[f.key(scope, name)]
	if !ok {
		return agentstate.Entry{}, agentstate.ErrNotFound
	}
	return e, nil
}

func (f *fakeStore) List(_ context.Context, scope, prefix string) ([]agentstate.Entry, error) {
	out := []agentstate.Entry{}
	for _, e := range f.entries {
		if e.Scope == strings.ToUpper(scope) && strings.HasPrefix(e.Name, prefix) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeStore) Delete(_ context.Context, scope, name string) (bool, error) {
	k := f.key(scope, name)
	if _, ok := f.entries[k]; !ok {
		return false, nil
	}
	delete(f.entries, k)
	return true, nil
}

func (f *fakeStore) DeletePrefix(_ context.Context, scope, prefix string) (int, error) {
	n := 0
	for k, e := range f.entries {
		if e.Scope == strings.ToUpper(scope) && strings.HasPrefix(e.Name, prefix) {
			delete(f.entries, k)
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) DeleteScope(_ context.Context, scope string) (int, error) {
	n := 0
	for k, e := range f.entries {
		if e.Scope == strings.ToUpper(scope) {
			delete(f.entries, k)
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) Incr(_ context.Context, scope, name string, by int64, meta agentstate.Meta) (int64, error) {
	f.lastMeta = meta
	k := f.key(scope, name)
	current := int64(0)
	if e, ok := f.entries[k]; ok {
		current, _ = strconv.ParseInt(e.Value, 10, 64)
	}
	next := current + by
	f.entries[k] = agentstate.Entry{
		Scope: strings.ToUpper(scope), Name: name, Value: strconv.FormatInt(next, 10),
		Format: agentstate.FormatText, Agent: meta.Agent,
	}
	return next, nil
}

func (f *fakeStore) Lease(_ context.Context, req agentstate.LeaseRequest) (agentstate.LeaseResult, error) {
	f.lastMeta = req.Meta
	if f.claimResp != nil {
		return *f.claimResp, nil
	}
	c := agentstate.Lease{
		Scope: strings.ToUpper(req.Scope), Stage: req.Stage, Agent: req.Meta.Agent,
		LeasedAt: time.Unix(0, 0).UTC(), HeartbeatAt: time.Unix(0, 0).UTC(),
	}
	f.leases[f.key(req.Scope, req.Stage)] = c
	return agentstate.LeaseResult{Granted: true, Lease: c}, nil
}

func (f *fakeStore) Release(_ context.Context, scope, stage, agent string) (bool, error) {
	k := f.key(scope, stage)
	c, ok := f.leases[k]
	if !ok || (agent != "" && c.Agent != agent) {
		return false, nil
	}
	delete(f.leases, k)
	return true, nil
}

func (f *fakeStore) Leases(_ context.Context, scope string) ([]agentstate.Lease, error) {
	out := []agentstate.Lease{}
	for _, c := range f.leases {
		if c.Scope == strings.ToUpper(scope) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stage < out[j].Stage })
	return out, nil
}

func (f *fakeStore) Prune(_ context.Context, _ time.Time) (int, error) { return 3, nil }

func (f *fakeStore) Close() error {
	f.closed = true
	return nil
}

// run executes the state command against the fake store and returns stdout.
func run(t *testing.T, store *fakeStore, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	cmd := buildStateCmd(func() (agentstate.Store, error) { return store, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SetContext(ctx)
	err := cmd.Execute()
	return out.String(), err
}

func TestSet_ThenGet(t *testing.T) {
	store := newFakeStore()

	out, err := run(t, store, context.Background(), "set", "sc-1200", "fix.evidence", "--value", "root cause")
	require.NoError(t, err)
	require.Contains(t, out, "SC-1200 fix.evidence")

	out, err = run(t, store, context.Background(), "get", "SC-1200", "fix.evidence")
	require.NoError(t, err)
	require.Equal(t, "root cause\n", out)
}

func TestSet_ReadsValueFromStdin(t *testing.T) {
	store := newFakeStore()
	cmd := buildStateCmd(func() (agentstate.Store, error) { return store, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("{\"status\":\"confirmed\"}\n"))
	cmd.SetArgs([]string{"set", "SC-1", "stage.triage", "--body-file", "-", "--json"})
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.Execute())

	got, err := store.Get(context.Background(), "SC-1", "stage.triage")
	require.NoError(t, err)
	require.Equal(t, `{"status":"confirmed"}`, got.Value, "the trailing newline is trimmed")
	require.Equal(t, agentstate.FormatJSON, got.Format)
}

func TestSet_RejectsBothValueAndBodyFile(t *testing.T) {
	_, err := run(t, newFakeStore(), context.Background(),
		"set", "SC-1", "k", "--value", "v", "--body-file", "-")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not both")
}

// The writing agent's identity must come from the request context: inside the
// daemon, a board container's HUMAN_AGENT_NAME arrives there, not in the
// daemon's own process environment.
func TestSet_RecordsAgentFromContextEnv(t *testing.T) {
	store := newFakeStore()
	ctx := env.WithEnv(context.Background(), map[string]string{
		"HUMAN_AGENT_NAME": "board-fix-sc-1200",
		"HUMAN_DAEMON_ID":  "daemon-7",
	})

	_, err := run(t, store, ctx, "set", "SC-1200", "k", "--value", "v")
	require.NoError(t, err)
	require.Equal(t, "board-fix-sc-1200", store.lastMeta.Agent)
	require.Equal(t, "daemon-7", store.lastMeta.RunID)
}

// --agent overrides the environment so a caller can attribute a write on
// behalf of a named agent; attribution is what a successor inherits by.
func TestSet_AgentFlagOverridesContextEnv(t *testing.T) {
	store := newFakeStore()
	ctx := env.WithEnv(context.Background(), map[string]string{"HUMAN_AGENT_NAME": "from-env"})

	_, err := run(t, store, ctx, "set", "SC-1", "k", "--value", "v", "--agent", "alpha")
	require.NoError(t, err)
	require.Equal(t, "alpha", store.lastMeta.Agent)

	_, err = run(t, store, ctx, "incr", "SC-1", "budget.fix.attempts", "--agent", "alpha")
	require.NoError(t, err)
	require.Equal(t, "alpha", store.lastMeta.Agent)
}

func TestGet_MissingFailsUnlessDefaultGiven(t *testing.T) {
	store := newFakeStore()

	_, err := run(t, store, context.Background(), "get", "SC-1", "absent")
	require.Error(t, err)

	out, err := run(t, store, context.Background(), "get", "SC-1", "absent", "--default", "0")
	require.NoError(t, err)
	require.Equal(t, "0\n", out)
}

// A stage report must be consumable as data. Reading one key out of it is what
// replaces "the first line under ## Summary is the outcome".
func TestGet_FieldExtractsFromAJSONStageReport(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	report := `{"exit":"done","summary":"fix verified","attempts":2,"blockers":null}`
	_, err := store.Set(ctx, "SC-1", "stage.verify", report, agentstate.FormatJSON, agentstate.Meta{})
	require.NoError(t, err)

	out, err := run(t, store, ctx, "get", "SC-1", "stage.verify", "--field", "exit")
	require.NoError(t, err)
	require.Equal(t, "done\n", out, "a string field is printed bare so a shell can compare it")

	out, err = run(t, store, ctx, "get", "SC-1", "stage.verify", "--field", "attempts")
	require.NoError(t, err)
	require.Equal(t, "2\n", out)

	out, err = run(t, store, ctx, "get", "SC-1", "stage.verify", "--field", "blockers")
	require.NoError(t, err)
	require.Equal(t, "null\n", out)
}

func TestGet_MissingFieldFailsUnlessDefaultGiven(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	_, err := store.Set(ctx, "SC-1", "stage.verify", `{"exit":"done"}`, agentstate.FormatJSON, agentstate.Meta{})
	require.NoError(t, err)

	_, err = run(t, store, ctx, "get", "SC-1", "stage.verify", "--field", "absent")
	require.Error(t, err)

	out, err := run(t, store, ctx, "get", "SC-1", "stage.verify", "--field", "absent", "--default", "unknown")
	require.NoError(t, err)
	require.Equal(t, "unknown\n", out)
}

// Asking for a field of a non-JSON value is a mistake worth surfacing, not
// something to answer with an empty string.
func TestGet_FieldOnNonJSONValueFails(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	_, err := store.Set(ctx, "SC-1", "notes", "just prose", "", agentstate.Meta{})
	require.NoError(t, err)

	_, err = run(t, store, ctx, "get", "SC-1", "notes", "--field", "exit")
	require.Error(t, err)
}

func TestGet_MetaEmitsProvenanceJSON(t *testing.T) {
	store := newFakeStore()
	_, err := store.Set(context.Background(), "SC-1", "k", "v", "", agentstate.Meta{Agent: "alpha"})
	require.NoError(t, err)

	out, err := run(t, store, context.Background(), "get", "SC-1", "k", "--meta")
	require.NoError(t, err)

	var entry agentstate.Entry
	require.NoError(t, json.Unmarshal([]byte(out), &entry))
	require.Equal(t, "alpha", entry.Agent)
}

func TestList_TableAndPrefixAndJSON(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	for _, n := range []string{"budget.fix.attempts", "fix.evidence"} {
		_, err := store.Set(ctx, "SC-1", n, "v", "", agentstate.Meta{})
		require.NoError(t, err)
	}

	out, err := run(t, store, ctx, "list", "SC-1")
	require.NoError(t, err)
	require.Contains(t, out, "budget.fix.attempts")
	require.Contains(t, out, "fix.evidence")

	out, err = run(t, store, ctx, "list", "SC-1", "--prefix", "budget.")
	require.NoError(t, err)
	require.Contains(t, out, "budget.fix.attempts")
	require.NotContains(t, out, "fix.evidence")

	out, err = run(t, store, ctx, "list", "SC-1", "--json")
	require.NoError(t, err)
	var entries []agentstate.Entry
	require.NoError(t, json.Unmarshal([]byte(out), &entries))
	require.Len(t, entries, 2)
}

func TestList_EmptyScopeSaysSo(t *testing.T) {
	out, err := run(t, newFakeStore(), context.Background(), "list", "SC-NONE")
	require.NoError(t, err)
	require.Contains(t, out, "no state")
}

func TestRm_SingleAndAll(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	_, err := store.Set(ctx, "SC-1", "a", "1", "", agentstate.Meta{})
	require.NoError(t, err)
	_, err = store.Set(ctx, "SC-1", "b", "2", "", agentstate.Meta{})
	require.NoError(t, err)

	out, err := run(t, store, ctx, "rm", "SC-1", "a")
	require.NoError(t, err)
	require.Contains(t, out, "removed")

	out, err = run(t, store, ctx, "rm", "SC-1", "a")
	require.NoError(t, err)
	require.Contains(t, out, "no such entry")

	out, err = run(t, store, ctx, "rm", "SC-1", "--all")
	require.NoError(t, err)
	require.Contains(t, out, "removed 1 entries")
}

func TestRm_RequiresExactlyOneOfNameOrAll(t *testing.T) {
	store := newFakeStore()

	_, err := run(t, store, context.Background(), "rm", "SC-1")
	require.Error(t, err, "neither a name nor --all")

	_, err = run(t, store, context.Background(), "rm", "SC-1", "a", "--all")
	require.Error(t, err, "both a name and --all")
}

// A fresh run clears the previous run's budgets; without this they read as
// already spent and the run gives up before doing any work.
func TestRm_PrefixClearsANamespace(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()
	for _, n := range []string{"budget.fix.attempts", "budget.review.attempts", "fix.evidence"} {
		_, err := store.Set(ctx, "SC-1", n, "3", "", agentstate.Meta{})
		require.NoError(t, err)
	}

	out, err := run(t, store, ctx, "rm", "SC-1", "--prefix", "budget.")
	require.NoError(t, err)
	require.Contains(t, out, "removed 2 entries")

	left, err := store.List(ctx, "SC-1", "")
	require.NoError(t, err)
	require.Len(t, left, 1)
	require.Equal(t, "fix.evidence", left[0].Name)
}

func TestRm_RejectsMoreThanOneTarget(t *testing.T) {
	store := newFakeStore()
	for _, args := range [][]string{
		{"rm", "SC-1", "a", "--prefix", "budget."},
		{"rm", "SC-1", "--prefix", "budget.", "--all"},
		{"rm", "SC-1", "a", "--all"},
		{"rm", "SC-1"},
	} {
		_, err := run(t, store, context.Background(), args...)
		require.Error(t, err, "args %v must be refused", args)
	}
}

func TestIncr_PrintsRunningTotal(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()

	out, err := run(t, store, ctx, "incr", "SC-1", "budget.fix.attempts")
	require.NoError(t, err)
	require.Equal(t, "1\n", out)

	out, err = run(t, store, ctx, "incr", "SC-1", "budget.fix.attempts", "--by", "2")
	require.NoError(t, err)
	require.Equal(t, "3\n", out)
}

func TestClaim_GrantedPrintsHolder(t *testing.T) {
	store := newFakeStore()

	out, err := run(t, store, context.Background(), "lease", "SC-1", "--stage", "fix", "--agent", "alpha")
	require.NoError(t, err)
	require.Contains(t, out, "leased SC-1/fix as alpha")
}

// A refused lease must fail the command so a caller can branch on the exit
// code, while still naming the holder on stdout.
func TestClaim_RefusedNamesHolderAndFails(t *testing.T) {
	store := newFakeStore()
	store.claimResp = &agentstate.LeaseResult{
		Granted: false,
		Lease: agentstate.Lease{
			Scope: "SC-1", Stage: "fix", Agent: "alpha", HeartbeatAt: time.Unix(0, 0).UTC(),
		},
	}

	out, err := run(t, store, context.Background(), "lease", "SC-1", "--stage", "fix", "--agent", "beta")
	require.Error(t, err)
	require.Contains(t, out, "held by alpha")
}

func TestClaim_TakeoverReportsInheritedState(t *testing.T) {
	store := newFakeStore()
	displaced := agentstate.Lease{Scope: "SC-1", Stage: "fix", Agent: "alpha", HeartbeatAt: time.Unix(0, 0).UTC()}
	store.claimResp = &agentstate.LeaseResult{
		Granted:       true,
		Lease:         agentstate.Lease{Scope: "SC-1", Stage: "fix", Agent: "beta"},
		Displaced:     &displaced,
		InheritedKeys: []string{"fix.evidence", "stage.fix"},
	}

	out, err := run(t, store, context.Background(), "lease", "SC-1", "--stage", "fix", "--agent", "beta")
	require.NoError(t, err)
	require.Contains(t, out, "took over from alpha")
	require.Contains(t, out, "inherited state: fix.evidence, stage.fix")
}

func TestClaim_TakeoverWithNoInheritedStateSaysSo(t *testing.T) {
	store := newFakeStore()
	displaced := agentstate.Lease{Scope: "SC-1", Stage: "fix", Agent: "alpha"}
	store.claimResp = &agentstate.LeaseResult{
		Granted:   true,
		Lease:     agentstate.Lease{Scope: "SC-1", Stage: "fix", Agent: "beta"},
		Displaced: &displaced,
	}

	out, err := run(t, store, context.Background(), "lease", "SC-1", "--stage", "fix", "--agent", "beta")
	require.NoError(t, err)
	require.Contains(t, out, "it left no state behind")
}

func TestClaim_JSONOutput(t *testing.T) {
	out, err := run(t, newFakeStore(), context.Background(),
		"lease", "SC-1", "--stage", "fix", "--agent", "alpha", "--json")
	require.NoError(t, err)

	var res agentstate.LeaseResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.True(t, res.Granted)
	require.Equal(t, "alpha", res.Lease.Agent)
}

func TestClaim_StageIsRequired(t *testing.T) {
	_, err := run(t, newFakeStore(), context.Background(), "lease", "SC-1")
	require.Error(t, err)
}

func TestRelease_ReportsWhetherAClaimWasLive(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()

	_, err := run(t, store, ctx, "lease", "SC-1", "--stage", "fix", "--agent", "alpha")
	require.NoError(t, err)

	out, err := run(t, store, ctx, "release", "SC-1", "--stage", "fix", "--agent", "alpha")
	require.NoError(t, err)
	require.Contains(t, out, "released")

	out, err = run(t, store, ctx, "release", "SC-1", "--stage", "fix", "--agent", "alpha")
	require.NoError(t, err)
	require.Contains(t, out, "no live lease")
}

func TestClaims_TableAndEmpty(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()

	out, err := run(t, store, ctx, "leases", "SC-1")
	require.NoError(t, err)
	require.Contains(t, out, "no leases")

	_, err = run(t, store, ctx, "lease", "SC-1", "--stage", "fix", "--agent", "alpha")
	require.NoError(t, err)

	out, err = run(t, store, ctx, "leases", "SC-1")
	require.NoError(t, err)
	require.Contains(t, out, "fix")
	require.Contains(t, out, "alpha")

	out, err = run(t, store, ctx, "leases", "SC-1", "--json")
	require.NoError(t, err)
	var leases []agentstate.Lease
	require.NoError(t, json.Unmarshal([]byte(out), &leases))
	require.Len(t, leases, 1)
}

func TestPrune_ReportsCount(t *testing.T) {
	out, err := run(t, newFakeStore(), context.Background(), "prune")
	require.NoError(t, err)
	require.Contains(t, out, "pruned 3 entries")
}

func TestStateCmd_ClosesTheStore(t *testing.T) {
	store := newFakeStore()

	_, err := run(t, store, context.Background(), "list", "SC-1")
	require.NoError(t, err)
	require.True(t, store.closed, "the store handle must not leak")
}

func TestBuildStateCmd_RegistersEveryVerb(t *testing.T) {
	cmd := BuildStateCmd()

	got := map[string]bool{}
	for _, sub := range cmd.Commands() {
		got[sub.Name()] = true
	}
	for _, want := range []string{"set", "get", "list", "rm", "incr", "lease", "release", "leases", "prune"} {
		require.True(t, got[want], "missing subcommand %q", want)
	}
}
