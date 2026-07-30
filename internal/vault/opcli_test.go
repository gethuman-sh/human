package vault

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/platform"
)

func TestOpCLI_CanResolve(t *testing.T) {
	op := NewOpCLI()
	assert.True(t, op.CanResolve("1pw://DevVault/GitHub PAT/token"))
	assert.True(t, op.CanResolve("1pw://vault/item/field"))
	assert.False(t, op.CanResolve("op://vault/item/field"))
	assert.False(t, op.CanResolve("ghp_abc123"))
	assert.False(t, op.CanResolve(""))
}

func TestNewOpCLI_binaryByPlatform(t *testing.T) {
	op := NewOpCLI()
	if platform.IsWSL() {
		assert.Equal(t, "op.exe", op.Binary)
	} else {
		assert.Equal(t, "op", op.Binary)
	}
}

func TestOpCLI_Resolve_success(t *testing.T) {
	op := &OpCLI{
		Binary: "op.exe",
		runner: func(_ context.Context, binary string, args ...string) ([]byte, error) {
			assert.Equal(t, "op.exe", binary)
			assert.Equal(t, []string{"read", "op://DevVault/GitHub PAT/token"}, args)
			return []byte("my-secret-token\n"), nil
		},
	}

	val, err := op.Resolve("1pw://DevVault/GitHub PAT/token")
	require.NoError(t, err)
	assert.Equal(t, "my-secret-token", val)
}

func TestOpCLI_Resolve_error(t *testing.T) {
	op := &OpCLI{
		Binary: "op.exe",
		runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return nil, errors.WithDetails("command failed")
		},
	}

	_, err := op.Resolve("1pw://vault/item/field")
	require.Error(t, err)
	// The message must identify the binary, the reference and the cause: it is
	// the only part that survives to a board banner, and the old wording
	// ("resolving 1Password secret via CLI") named none of the three (SC-2005).
	assert.Contains(t, err.Error(), "op.exe")
	assert.Contains(t, err.Error(), "1pw://vault/item/field")
	assert.Contains(t, err.Error(), "command failed")
}

func TestOpCLI_Resolve_translatesPrefix(t *testing.T) {
	var capturedArgs []string
	op := &OpCLI{
		Binary: "op.exe",
		runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			capturedArgs = args
			return []byte("the-password"), nil
		},
	}

	val, err := op.Resolve("1pw://MyVault/MyItem/password")
	require.NoError(t, err)
	assert.Equal(t, "the-password", val)
	assert.Equal(t, []string{"read", "op://MyVault/MyItem/password"}, capturedArgs)
}
