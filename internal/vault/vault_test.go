package vault

import (
	"context"
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

// fakeProvider implements SecretProvider for testing.
type fakeProvider struct {
	canResolve func(ref string) bool
	resolve    func(ref string) (string, error)
}

func (f *fakeProvider) CanResolve(ref string) bool         { return f.canResolve(ref) }
func (f *fakeProvider) Resolve(ref string) (string, error) { return f.resolve(ref) }
