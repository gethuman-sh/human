package vault

import (
	"context"
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

// Reproduces SC-1653: on a released (CGO_ENABLED=0) non-WSL build the SDK
// provider fails to initialize; the resolver must fall through to the op CLI
// behind it rather than surfacing "initializing 1Password SDK".
func TestResolver_Resolve_fallsThroughToCLIWhenSDKFails(t *testing.T) {
	sdk := NewOnePassword("my-account")
	sdk.clientFactory = func(_ context.Context) (secretResolver, error) {
		return nil, errors.WithDetails("initializing 1Password SDK")
	}
	cli := &OpCLI{
		Binary: "op",
		runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			assert.Equal(t, []string{"read", "op://Development/GitHub PAT/token"}, args)
			return []byte("resolved-secret\n"), nil
		},
	}
	r := NewResolver(sdk, cli, NewGhCLI())

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

// A successful resolution refreshes the cache: a value read again while the
// store is up wins over an older cached one, and rotations are picked up.
func TestResolver_Resolve_successRefreshesCache(t *testing.T) {
	current := "first"
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve:    func(string) (string, error) { return current, nil },
	}
	r := NewResolver(provider)

	val, err := r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "first", val)

	current = "rotated"
	val, err = r.Resolve("1pw://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "rotated", val)
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
