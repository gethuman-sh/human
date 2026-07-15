package vault

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

func TestGhCLI_CanResolve(t *testing.T) {
	gh := NewGhCLI()
	assert.True(t, gh.CanResolve("gh://token"))
	assert.True(t, gh.CanResolve("gh://github.example.com/token"))
	assert.False(t, gh.CanResolve("1pw://vault/item/field"))
	assert.False(t, gh.CanResolve("ghp_abc123"))
	assert.False(t, gh.CanResolve(""))
}

func TestGhCLI_Resolve_defaultHost(t *testing.T) {
	gh := &GhCLI{
		Binary: "gh",
		runner: func(_ context.Context, binary string, args ...string) ([]byte, error) {
			assert.Equal(t, "gh", binary)
			assert.Equal(t, []string{"auth", "token"}, args)
			return []byte("gho_secret\n"), nil
		},
	}

	val, err := gh.Resolve("gh://token")
	require.NoError(t, err)
	assert.Equal(t, "gho_secret", val)
}

func TestGhCLI_Resolve_withHostname(t *testing.T) {
	gh := &GhCLI{
		Binary: "gh",
		runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			assert.Equal(t, []string{"auth", "token", "--hostname", "github.example.com"}, args)
			return []byte("ghe_secret\n"), nil
		},
	}

	val, err := gh.Resolve("gh://github.example.com/token")
	require.NoError(t, err)
	assert.Equal(t, "ghe_secret", val)
}

func TestGhCLI_Resolve_invalidRef(t *testing.T) {
	gh := NewGhCLI()
	// The grammar is the injection guard: anything but a bare hostname
	// segment plus the literal "token" field must be rejected before gh runs.
	for _, ref := range []string{
		"gh://",
		"gh://password",
		"gh://host/other",
		"gh://--hostname/token",
		"gh://a b/token",
		"gh://host/token/extra",
	} {
		_, err := gh.Resolve(ref)
		require.Error(t, err, "ref %q must be rejected", ref)
		assert.Contains(t, err.Error(), "invalid secret reference")
	}
}

func TestGhCLI_Resolve_error(t *testing.T) {
	gh := &GhCLI{
		Binary: "gh",
		runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return nil, errors.WithDetails("command failed")
		},
	}

	_, err := gh.Resolve("gh://token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving GitHub token via gh CLI")
}

func TestGhCLI_Resolve_emptyToken(t *testing.T) {
	gh := &GhCLI{
		Binary: "gh",
		runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("\n"), nil
		},
	}

	_, err := gh.Resolve("gh://token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty token")
}

func TestIsSecretRef_gh(t *testing.T) {
	assert.True(t, IsSecretRef("gh://token"))
	assert.False(t, IsSecretRef("gh:token"))
}

func TestNewResolverFromConfig_github(t *testing.T) {
	r := NewResolverFromConfig(&Config{Provider: "github"})
	require.NotNil(t, r)

	// A non-ref value passes through untouched — same contract as 1password.
	val, err := r.Resolve("plain-value")
	require.NoError(t, err)
	assert.Equal(t, "plain-value", val)
}

func TestNewResolverFromConfig_onePasswordIncludesGh(t *testing.T) {
	r := NewResolverFromConfig(&Config{Provider: "1password", Account: "acct"})
	require.NotNil(t, r)

	// gh:// must be claimed by a provider under a 1password config; an
	// unclaimed ref would echo back verbatim and leak into API calls as a
	// fake token. The error (no gh session in CI) or a real token both prove
	// the GhCLI provider claimed it — verbatim echo would prove it didn't.
	val, err := r.Resolve("gh://token")
	if err == nil {
		assert.NotEqual(t, "gh://token", val)
	}
}
