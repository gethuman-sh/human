package vault

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

// fakeSecretResolver implements secretResolver for testing.
type fakeSecretResolver struct {
	results map[string]string
	err     error
	calls   int
	lastRef string
}

func (f *fakeSecretResolver) Resolve(_ context.Context, ref string) (string, error) {
	f.calls++
	f.lastRef = ref
	if f.err != nil {
		return "", f.err
	}
	if val, ok := f.results[ref]; ok {
		return val, nil
	}
	return "", errors.WithDetails("secret not found", "ref", ref)
}

func TestOnePassword_CanResolve(t *testing.T) {
	op := NewOnePassword("test-account")
	assert.True(t, op.CanResolve("1pw://DevVault/GitHub PAT/token"))
	assert.True(t, op.CanResolve("1pw://vault/item/field"))
	assert.False(t, op.CanResolve("op://vault/item/field"))
	assert.False(t, op.CanResolve("ghp_abc123"))
	assert.False(t, op.CanResolve(""))
}

func TestOnePassword_Resolve_success(t *testing.T) {
	resolver := &fakeSecretResolver{
		results: map[string]string{
			// SDK receives op:// after translation
			"op://DevVault/GitHub PAT/token": "my-secret-token",
		},
	}
	op := &OnePassword{
		clientFactory: func(_ context.Context) (secretResolver, error) {
			return resolver, nil
		},
	}

	val, err := op.Resolve("1pw://DevVault/GitHub PAT/token")
	require.NoError(t, err)
	assert.Equal(t, "my-secret-token", val)
	// Verify the ref was translated to op:// for the SDK
	assert.Equal(t, "op://DevVault/GitHub PAT/token", resolver.lastRef)
}

func TestOnePassword_Resolve_error(t *testing.T) {
	resolver := &fakeSecretResolver{
		err: errors.WithDetails("SDK error"),
	}
	op := &OnePassword{
		clientFactory: func(_ context.Context) (secretResolver, error) {
			return resolver, nil
		},
	}

	_, err := op.Resolve("1pw://DevVault/GitHub PAT/token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving 1Password secret")
}

func TestOnePassword_Resolve_clientInitError(t *testing.T) {
	op := &OnePassword{
		clientFactory: func(_ context.Context) (secretResolver, error) {
			return nil, errors.WithDetails("auth failed")
		},
	}

	_, err := op.Resolve("1pw://vault/item/field")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initializing 1Password SDK")
}

func TestOnePassword_Resolve_lazyInit(t *testing.T) {
	initCalls := 0
	resolver := &fakeSecretResolver{
		results: map[string]string{
			"op://vault/item/field": "secret",
		},
	}
	op := &OnePassword{
		clientFactory: func(_ context.Context) (secretResolver, error) {
			initCalls++
			return resolver, nil
		},
	}

	// Multiple resolves should only init once.
	_, _ = op.Resolve("1pw://vault/item/field")
	_, _ = op.Resolve("1pw://vault/item/field")
	assert.Equal(t, 1, initCalls)
}

func TestOnePassword_Resolve_translatesPrefix(t *testing.T) {
	resolver := &fakeSecretResolver{
		results: map[string]string{
			"op://MyVault/MyItem/password": "the-password",
		},
	}
	op := &OnePassword{
		clientFactory: func(_ context.Context) (secretResolver, error) {
			return resolver, nil
		},
	}

	val, err := op.Resolve("1pw://MyVault/MyItem/password")
	require.NoError(t, err)
	assert.Equal(t, "the-password", val)
	assert.Equal(t, "op://MyVault/MyItem/password", resolver.lastRef)
}
