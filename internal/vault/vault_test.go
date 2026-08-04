package vault

import (
	"context"
	stderrors "errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

func TestIsSecretRef(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"1pw://DevVault/GitHub PAT/token", true},
		{"1pw://vault/item/field", true},
		{"ghp_abc123", false},
		{"", false},
		{"OP://uppercase", false}, // case-sensitive
		{"token-value", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, IsSecretRef(tt.input))
		})
	}
}

func TestResolver_Resolve_nonRef(t *testing.T) {
	r := NewResolver()

	val, err := r.Resolve("plain-token")
	require.NoError(t, err)
	assert.Equal(t, "plain-token", val)
}

func TestResolver_Resolve_noProviderReturnsAsIs(t *testing.T) {
	// No providers registered that can handle the ref.
	provider := &fakeProvider{
		canResolve: func(ref string) bool { return false },
		resolve:    func(ref string) (string, error) { return "", nil },
	}
	r := NewResolver(provider)

	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "1pw://vault/item/field", val)
}

func TestResolver_Resolve_multipleProviders(t *testing.T) {
	first := &fakeProvider{
		canResolve: func(ref string) bool { return false },
		resolve:    func(ref string) (string, error) { return "wrong", nil },
	}
	second := &fakeProvider{
		canResolve: func(ref string) bool { return true },
		resolve:    func(ref string) (string, error) { return "correct", nil },
	}
	r := NewResolver(first, second)

	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "correct", val)
}

// Reproduces SC-1653, which was a released build failing to resolve because the
// in-process SDK sat in front of the op CLI and could not initialise. The SDK
// is gone (SC-2183) and this is what replaced it: one provider, the same on
// every build, translating the user-facing 1pw:// reference into the op:// form
// the CLI expects.
func TestResolver_Resolve_onePasswordRefGoesThroughTheCLI(t *testing.T) {
	cli := &OpCLI{
		Binary: "op",
		runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			assert.Equal(t, []string{"read", "op://Development/GitHub PAT/token"}, args)
			return []byte("resolved-secret\n"), nil
		},
	}
	r := NewResolver(cli, NewGhCLI())

	val, err := r.Resolve("1pw://Development/GitHub PAT/token")

	require.NoError(t, err)
	assert.Equal(t, "resolved-secret", val)
}

// When every claiming provider errors, Resolve returns the last error.
func TestResolver_Resolve_allClaimantsFail(t *testing.T) {
	first := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve:    func(string) (string, error) { return "", errors.WithDetails("first failed") },
	}
	second := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve:    func(string) (string, error) { return "", errors.WithDetails("last failed") },
	}
	r := NewResolver(first, second)

	_, err := r.Resolve("1pw://vault/item/field")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "last failed")
}

// SC-2039: once a secret resolves, a later credential lapse (every claimant
// erroring) must serve the still-fresh cached value instead of failing a step
// whose secret already resolved this run.
func TestResolver_Resolve_lapseServesCachedValue(t *testing.T) {
	fail := false
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			if fail {
				return "", errors.WithDetails("session expired")
			}
			return "secret", nil
		},
	}
	r := NewResolver(provider)

	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "secret", val)

	// The store lapses; the value was resolved moments ago and is unchanged.
	fail = true
	val, err = r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "secret", val)
}

// A lapse with nothing cached still surfaces as the underlying error.
func TestResolver_Resolve_lapseWithoutCacheErrors(t *testing.T) {
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve:    func(string) (string, error) { return "", errors.WithDetails("session expired") },
	}
	r := NewResolver(provider)

	_, err := r.Resolve("1pw://vault/item/field")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session expired")
}

// An expired cache entry is not served: plaintext must not outlive its TTL.
func TestResolver_Resolve_expiredCacheNotServed(t *testing.T) {
	fail := false
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			if fail {
				return "", errors.WithDetails("session expired")
			}
			return "secret", nil
		},
	}
	now := time.Unix(0, 0)
	r := NewResolver(provider)
	r.now = func() time.Time { return now }
	r.ttl = time.Minute

	_, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)

	// Advance past the TTL, then lapse: the stale entry must be dropped, not served.
	now = now.Add(2 * time.Minute)
	fail = true
	_, err = r.Resolve("1pw://vault/item/field")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session expired")
}

// SC-2039 review follow-up: an entry whose own ref is never re-requested
// must still be evicted once its TTL passes, not held for the process
// lifetime. remember() sweeps every expired entry (not just the one it is
// storing), so a later successful resolve of a *different* ref evicts it.
func TestResolver_Resolve_expiredEntryNotRetainedWithoutBeingReRequested(t *testing.T) {
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve:    func(string) (string, error) { return "secret", nil },
	}
	now := time.Unix(0, 0)
	r := NewResolver(provider)
	r.now = func() time.Time { return now }
	r.ttl = time.Minute

	// Resolve a ref once; it is never asked for again.
	_, err := r.Resolve("1pw://vault/stale/field")
	require.NoError(t, err)
	require.Len(t, r.cache, 1)

	// Advance past the TTL and resolve an unrelated ref. The stale entry
	// must be swept even though nothing ever re-requested it.
	now = now.Add(2 * time.Minute)
	_, err = r.Resolve("1pw://vault/other/field")
	require.NoError(t, err)

	r.mu.Lock()
	_, stillCached := r.cache["1pw://vault/stale/field"]
	r.mu.Unlock()
	assert.False(t, stillCached, "expired entry must not be retained past its TTL")
}

// A non-positive TTL disables caching, restoring strict no-persistence.
func TestResolver_Resolve_cachingDisabledByZeroTTL(t *testing.T) {
	fail := false
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			if fail {
				return "", errors.WithDetails("session expired")
			}
			return "secret", nil
		},
	}
	r := NewResolver(provider)
	r.ttl = 0

	_, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)

	fail = true
	_, err = r.Resolve("1pw://vault/item/field")
	require.Error(t, err)
}

// The contract that keeps a human out of the machine's loop: within the TTL the
// stored value is served and the provider is never consulted. With an
// interactive store, every provider call is an approval dialog, so "ask again
// each time" means one prompt per command the pipeline runs (SC-2173).
func TestResolver_Resolve_servesTheCachedValueWithoutAskingAgain(t *testing.T) {
	calls := 0
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			calls++
			return "token", nil
		},
	}
	r := NewResolver(provider)

	for range 5 {
		val, err := r.Resolve("1pw://vault/item/field")
		require.NoError(t, err)
		assert.Equal(t, "token", val)
	}

	assert.Equal(t, 1, calls, "five reads of one secret must raise one approval, not five")
}

// The trade-off, stated as a test rather than left implicit: a rotated secret
// is picked up when the entry ages out, not instantly.
func TestResolver_Resolve_rotationIsPickedUpWhenTheEntryAgesOut(t *testing.T) {
	now := time.Now()
	current := "first"
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve:    func(string) (string, error) { return current, nil },
	}
	r := NewResolver(provider)
	r.now = func() time.Time { return now }
	r.ttl = time.Minute

	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "first", val)

	current = "rotated"
	val, err = r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "first", val, "within the window the stored value stands")

	now = now.Add(time.Minute + time.Second)
	val, err = r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "rotated", val, "once it ages out the store is asked again")
}

// SC-3321: the window slides on use. A secret read continuously must not age
// out on a schedule fixed at fetch time — that is what put a person back in the
// machine's loop four times an hour, approving a dialog for a credential the
// pipeline had been using all along.
func TestResolver_Resolve_hitSlidesIdleWindow(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	fail := false
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			if fail {
				return "", errors.WithDetails("would raise an approval dialog")
			}
			calls++
			return "token", nil
		},
	}
	r := NewResolver(provider)
	r.now = func() time.Time { return now }
	r.ttl = time.Minute
	r.maxTTL = time.Hour

	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "token", val)

	// A read inside the window pushes the deadline out to t0+40s+1m.
	now = now.Add(40 * time.Second)
	val, err = r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "token", val)

	// Past the original t0+1m deadline, but inside the slid one: still served,
	// and the provider (an approval dialog) is never reached.
	now = now.Add(50 * time.Second)
	fail = true
	val, err = r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "token", val)
	assert.Equal(t, 1, calls, "a secret in continuous use must not re-consult the store")
}

// The other half of the contract: idleness, not age, retires an entry. A secret
// nobody asks for stops being held one window after its last use.
func TestResolver_Resolve_idleSecretRetiredByLapse(t *testing.T) {
	now := time.Unix(0, 0)
	fail := false
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			if fail {
				return "", errors.WithDetails("session expired")
			}
			return "secret", nil
		},
	}
	r := NewResolver(provider)
	r.now = func() time.Time { return now }
	r.ttl = time.Minute
	r.maxTTL = time.Hour

	_, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)

	// Nothing reads it for two windows, so nothing slid it.
	now = now.Add(2 * time.Minute)
	fail = true
	_, err = r.Resolve("1pw://vault/item/field")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session expired")
}

// SC-2183 reconciliation: sliding must not mean "held forever". However
// continuously a secret is read, the absolute ceiling set at creation stands.
func TestResolver_Resolve_cappedByMaxLifetime(t *testing.T) {
	now := time.Unix(0, 0)
	current := "first"
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve:    func(string) (string, error) { return current, nil },
	}
	r := NewResolver(provider)
	r.now = func() time.Time { return now }
	r.ttl = time.Minute
	r.maxTTL = 3 * time.Minute

	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "first", val)

	current = "rotated"
	// Read every 50s: each hit slides the idle window, so the entry survives
	// well past a single window — but never past t0+3m.
	for _, elapsed := range []time.Duration{50 * time.Second, 100 * time.Second, 150 * time.Second} {
		now = time.Unix(0, 0).Add(elapsed)
		val, err = r.Resolve("1pw://vault/item/field")
		require.NoError(t, err)
		assert.Equal(t, "first", val, "continuous use keeps the entry alive at %s", elapsed)
	}

	now = time.Unix(0, 0).Add(200 * time.Second)
	val, err = r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "rotated", val, "the ceiling stands however continuously the secret is read")
}

// A cache_max_ttl shorter than cache_ttl has no coherent meaning, so it is
// clamped up to one idle window rather than retiring entries early — a config
// that only ever set cache_ttl must not start expiring sooner than it did.
func TestResolver_Resolve_maxClampedToIdleWindow(t *testing.T) {
	now := time.Unix(0, 0)
	fail := false
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			if fail {
				return "", errors.WithDetails("session expired")
			}
			return "secret", nil
		},
	}
	r := NewResolver(provider)
	r.now = func() time.Time { return now }
	r.ttl = 10 * time.Minute
	r.maxTTL = time.Minute // misconfigured: shorter than the idle window

	_, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)

	// Well past the bogus ceiling but inside the idle window: still served.
	now = now.Add(5 * time.Minute)
	fail = true
	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "secret", val, "the entry must live at least one idle window")

	// Past the idle window it goes, as always.
	now = now.Add(11 * time.Minute)
	_, err = r.Resolve("1pw://vault/item/field")
	require.Error(t, err)
}

// The disable path is untouched by the sliding window: with cache_ttl at zero
// nothing is stored, whatever the ceiling says.
func TestResolver_Resolve_zeroTTLStillDisablesCache(t *testing.T) {
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve:    func(string) (string, error) { return "secret", nil },
	}
	r := NewResolver(provider)
	r.ttl = 0
	r.maxTTL = time.Hour

	_, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)

	r.mu.Lock()
	defer r.mu.Unlock()
	assert.Empty(t, r.cache, "a non-positive cache_ttl must persist no plaintext")
}

// Disabling the cache restores asking every time, for anyone who wants that.
func TestResolver_Resolve_disabledCacheAsksEveryTime(t *testing.T) {
	calls := 0
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			calls++
			return "token", nil
		},
	}
	r := NewResolver(provider)
	r.ttl = 0

	for range 3 {
		_, err := r.Resolve("1pw://vault/item/field")
		require.NoError(t, err)
	}

	assert.Equal(t, 3, calls)
}

// Several pieces of work starting at once must raise ONE approval between them.
// A person cannot clear ten dialogs before the first times out, so a burst is
// exactly when asking separately fails.
func TestResolver_Resolve_concurrentCallersShareOneApproval(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			calls.Add(1)
			<-release // hold the "dialog" open while the others pile in
			return "token", nil
		},
	}
	r := NewResolver(provider)

	var wg sync.WaitGroup
	got := make([]string, 10)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			val, err := r.Resolve("1pw://vault/item/field")
			assert.NoError(t, err)
			got[i] = val
		}()
	}
	// Let the waiters reach the in-flight call before the first one returns.
	assert.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int32(1), calls.Load(), "ten concurrent readers, one approval")
	for _, v := range got {
		assert.Equal(t, "token", v, "every caller gets the resolved value")
	}
}

// Distinct references are not collapsed — the single-flight is per secret.
func TestResolver_Resolve_differentRefsAreNotShared(t *testing.T) {
	calls := 0
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(ref string) (string, error) {
			calls++
			return ref, nil
		},
	}
	r := NewResolver(provider)

	a, err := r.Resolve("1pw://vault/item/one")
	require.NoError(t, err)
	b, err := r.Resolve("1pw://vault/item/two")
	require.NoError(t, err)

	assert.Equal(t, "1pw://vault/item/one", a)
	assert.Equal(t, "1pw://vault/item/two", b)
	assert.Equal(t, 2, calls)
}

func TestResolveField_nilResolver(t *testing.T) {
	val, err := ResolveField(nil, "1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "1pw://vault/item/field", val)
}

func TestResolveField_withResolver(t *testing.T) {
	provider := &fakeProvider{
		canResolve: func(ref string) bool { return true },
		resolve:    func(ref string) (string, error) { return "resolved", nil },
	}
	r := NewResolver(provider)

	val, err := ResolveField(r, "1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "resolved", val)
}

func TestResolveField_plainValue(t *testing.T) {
	provider := &fakeProvider{
		canResolve: func(ref string) bool { return true },
		resolve:    func(ref string) (string, error) { return "resolved", nil },
	}
	r := NewResolver(provider)

	val, err := ResolveField(r, "ghp_abc")
	require.NoError(t, err)
	assert.Equal(t, "ghp_abc", val)
}

// SC-3322 regression guard: one failed read must not be re-asked at poll
// rate. Before the fix, a reference whose read fails is indistinguishable
// from one never read, so the very next call reaches the provider again —
// which is how one unanswered approval dialog became 705 queued prompts.
func TestResolver_Resolve_failedReadIsNotAskedAgainImmediately(t *testing.T) {
	calls := 0
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			calls++
			return "", tagCause(ErrStoreUnreachable, "op timed out")
		},
	}
	r := NewResolver(provider)

	_, err := r.Resolve("1pw://vault/item/field")
	require.Error(t, err)

	_, err = r.Resolve("1pw://vault/item/field")
	require.Error(t, err)

	assert.Equal(t, 1, calls, "a second read inside the hold must not reach the provider again")
}

// The hold expires and the store is consulted again once it passes.
func TestResolver_Resolve_holdExpiresAndTheStoreIsConsultedAgain(t *testing.T) {
	calls := 0
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			calls++
			return "", tagCause(ErrStoreUnreachable, "op timed out")
		},
	}
	now := time.Unix(0, 0)
	r := NewResolver(provider)
	r.now = func() time.Time { return now }

	_, err := r.Resolve("1pw://vault/item/field")
	require.Error(t, err)
	assert.Equal(t, 1, calls)

	now = now.Add(FailureHoldInitial + time.Second)
	_, err = r.Resolve("1pw://vault/item/field")
	require.Error(t, err)
	assert.Equal(t, 2, calls, "once the hold passes the store must be consulted again")
}

// Consecutive failures back off with doubling holds, capped at FailureHoldMax.
func TestResolver_Resolve_consecutiveFailuresBackOffAndCap(t *testing.T) {
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			return "", tagCause(ErrStoreUnreachable, "op timed out")
		},
	}
	now := time.Unix(0, 0)
	r := NewResolver(provider)
	r.now = func() time.Time { return now }

	wantHolds := []time.Duration{
		FailureHoldInitial,     // 30s
		2 * FailureHoldInitial, // 1m
		4 * FailureHoldInitial, // 2m
		8 * FailureHoldInitial, // 4m
		FailureHoldMax,         // capped at 5m
	}
	for _, want := range wantHolds {
		_, err := r.Resolve("1pw://vault/item/field")
		require.Error(t, err)

		r.mu.Lock()
		got := r.failures["1pw://vault/item/field"].hold
		r.mu.Unlock()
		assert.Equal(t, want, got)

		// Advance past this hold so the next Resolve reaches the provider again.
		now = now.Add(got + time.Second)
	}
}

// Recovery resets the backoff: the next outage starts again from
// FailureHoldInitial rather than from wherever the previous run left off.
func TestResolver_Resolve_recoveryResetsTheBackoff(t *testing.T) {
	fail := true
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			if fail {
				return "", tagCause(ErrStoreUnreachable, "op timed out")
			}
			return "secret", nil
		},
	}
	now := time.Unix(0, 0)
	r := NewResolver(provider)
	r.now = func() time.Time { return now }

	_, err := r.Resolve("1pw://vault/item/field")
	require.Error(t, err)
	now = now.Add(FailureHoldInitial + time.Second)

	// Recover.
	fail = false
	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "secret", val)

	// Fail again immediately after — since the value cache is fresh, force a
	// fresh outage by expiring the cached value first.
	r.mu.Lock()
	delete(r.cache, "1pw://vault/item/field")
	r.mu.Unlock()

	fail = true
	_, err = r.Resolve("1pw://vault/item/field")
	require.Error(t, err)

	r.mu.Lock()
	got := r.failures["1pw://vault/item/field"].hold
	r.mu.Unlock()
	assert.Equal(t, FailureHoldInitial, got, "a fresh outage after a recovery must not inherit the previous doubled hold")
}

// SC-2039 regression: a still-fresh cached value must be served even when a
// failure is on record for the same reference — the cache is consulted first.
func TestResolver_Resolve_freshCachedValueBeatsAHeldFailure(t *testing.T) {
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			t.Fatal("provider must not be consulted when a fresh value is cached")
			return "", nil
		},
	}
	now := time.Unix(0, 0)
	r := NewResolver(provider)
	r.now = func() time.Time { return now }
	r.ttl = time.Minute
	// The absolute deadline must be seeded too, and in the future: cached()
	// drops any entry whose deadline has passed (SC-3321), so a zero deadline
	// would evict this entry on read and leave the test asserting nothing about
	// the cache-beats-hold ordering it exists to guard.
	r.cache["1pw://vault/item/field"] = cachedSecret{
		value:     seal("cached-value"),
		expiresAt: now.Add(time.Minute),
		deadline:  now.Add(DefaultMaxCacheTTL),
	}
	r.failures["1pw://vault/item/field"] = failureState{
		err:     tagCause(ErrStoreUnreachable, "op timed out"),
		hold:    FailureHoldInitial,
		retryAt: now.Add(FailureHoldInitial),
	}

	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "cached-value", val)
}

// Where the two windows meet: the sliding idle window (SC-3321) must not let a
// held failure (SC-3322) serve plaintext past the absolute ceiling. Once the
// deadline passes, the entry is gone, so the hold surfaces as the failure it is
// rather than resurrecting a secret that should no longer be in memory.
func TestResolver_Resolve_heldFailureDoesNotResurrectAnEntryPastItsDeadline(t *testing.T) {
	now := time.Unix(0, 0)
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			t.Fatal("the store must not be consulted while the hold is on")
			return "", nil
		},
	}
	r := NewResolver(provider)
	r.now = func() time.Time { return now }
	r.ttl = time.Minute
	r.maxTTL = time.Hour
	// Idle window still open, absolute deadline already passed — the state the
	// sliding window produces for a secret used right up to its ceiling.
	r.cache["1pw://vault/item/field"] = cachedSecret{
		value:     seal("cached-value"),
		expiresAt: now.Add(time.Minute),
		deadline:  now.Add(-time.Second),
	}
	r.failures["1pw://vault/item/field"] = failureState{
		err:     tagCause(ErrStoreUnreachable, "op timed out"),
		hold:    FailureHoldInitial,
		retryAt: now.Add(FailureHoldInitial),
	}

	_, err := r.Resolve("1pw://vault/item/field")
	require.Error(t, err, "a secret past its absolute ceiling must not be served")
	assert.True(t, stderrors.Is(err, ErrStoreUnreachable))
	assert.True(t, IsHeldOff(err), "the store is still being left alone, so the hold is what surfaces")

	r.mu.Lock()
	_, stillCached := r.cache["1pw://vault/item/field"]
	r.mu.Unlock()
	assert.False(t, stillCached, "the expired entry must be dropped, not left in memory")
}

// A successful read clears the failure memory even though the sliding window
// rewrote remember(): the recovery path must survive the SC-3321 merge.
func TestResolver_Resolve_successAfterHoldClearsFailureAndCachesWithBothDeadlines(t *testing.T) {
	now := time.Unix(0, 0)
	fail := true
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			if fail {
				return "", tagCause(ErrStoreUnreachable, "op timed out")
			}
			return "token", nil
		},
	}
	r := NewResolver(provider)
	r.now = func() time.Time { return now }
	r.ttl = time.Minute
	r.maxTTL = time.Hour

	_, err := r.Resolve("1pw://vault/item/field")
	require.Error(t, err)

	// Past the hold, the store answers again.
	now = now.Add(FailureHoldInitial + time.Second)
	fail = false
	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "token", val)

	r.mu.Lock()
	_, heldStill := r.failures["1pw://vault/item/field"]
	entry := r.cache["1pw://vault/item/field"]
	r.mu.Unlock()
	assert.False(t, heldStill, "a successful read must clear the failure memory")
	assert.Equal(t, now.Add(time.Minute), entry.expiresAt, "the idle window starts from the successful read")
	assert.Equal(t, now.Add(time.Hour), entry.deadline, "the absolute ceiling is set at the successful read")
}

// The failure backoff is not gated on caching being enabled: an operator who
// disables the value cache (to keep plaintext out of memory) must still not
// be handed one approval prompt per call.
func TestResolver_Resolve_backoffAppliesWithCachingDisabled(t *testing.T) {
	calls := 0
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			calls++
			return "", tagCause(ErrStoreUnreachable, "op timed out")
		},
	}
	r := NewResolver(provider)
	r.ttl = 0

	_, err := r.Resolve("1pw://vault/item/field")
	require.Error(t, err)
	_, err = r.Resolve("1pw://vault/item/field")
	require.Error(t, err)

	assert.Equal(t, 1, calls, "the backoff must apply even with the value cache disabled")
}

// A held failure must keep its classified cause reachable via errors.Is, and
// must carry the held-off detail flag; the first, real failure must not.
func TestResolver_Resolve_heldFailureKeepsItsCause(t *testing.T) {
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			return "", tagCause(ErrStoreUnreachable, "op timed out")
		},
	}
	r := NewResolver(provider)

	_, firstErr := r.Resolve("1pw://vault/item/field")
	require.Error(t, firstErr)
	assert.True(t, stderrors.Is(firstErr, ErrStoreUnreachable))
	assert.True(t, IsSecretFailure(firstErr))
	assert.False(t, IsHeldOff(firstErr), "the first, real failure must not be reported as held off")

	_, heldErr := r.Resolve("1pw://vault/item/field")
	require.Error(t, heldErr)
	assert.True(t, stderrors.Is(heldErr, ErrStoreUnreachable))
	assert.True(t, IsSecretFailure(heldErr))
	assert.True(t, IsHeldOff(heldErr))
}

// The hold is per-reference: one held reference must not block resolution of
// an unrelated one. Extends TestResolver_Resolve_differentRefsAreNotShared.
func TestResolver_Resolve_holdIsPerReference(t *testing.T) {
	calls := map[string]int{}
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(ref string) (string, error) {
			calls[ref]++
			if ref == "1pw://vault/item/a" {
				return "", tagCause(ErrStoreUnreachable, "op timed out")
			}
			return "ok", nil
		},
	}
	r := NewResolver(provider)

	_, err := r.Resolve("1pw://vault/item/a")
	require.Error(t, err)
	_, err = r.Resolve("1pw://vault/item/a")
	require.Error(t, err)
	assert.Equal(t, 1, calls["1pw://vault/item/a"], "the held reference must not be re-asked")

	val, err := r.Resolve("1pw://vault/item/b")
	require.NoError(t, err)
	assert.Equal(t, "ok", val)
	assert.Equal(t, 1, calls["1pw://vault/item/b"], "an unrelated reference must resolve normally")
}

// fakeProvider implements SecretProvider for testing.
type fakeProvider struct {
	canResolve func(ref string) bool
	resolve    func(ref string) (string, error)
}

func (f *fakeProvider) CanResolve(ref string) bool         { return f.canResolve(ref) }
func (f *fakeProvider) Resolve(ref string) (string, error) { return f.resolve(ref) }
