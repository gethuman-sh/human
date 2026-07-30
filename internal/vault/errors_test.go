package vault

import (
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifySecretStderr(t *testing.T) {
	cases := []struct {
		diag string
		want error
	}{
		{"[ERROR] you are not currently signed in. Please run `op signin`", ErrNotAuthenticated},
		{"not logged in to any hosts. Run gh auth login", ErrNotAuthenticated},
		{`[ERROR] "Shortcut Token" isn't an item in the "Private" vault`, ErrSecretMissing},
		{"[ERROR] could not find vault \"Private\"", ErrSecretMissing},
		{"[ERROR] could not connect to 1Password desktop app", ErrStoreUnreachable},
		{"dial tcp: lookup my.1password.com: no such host", ErrStoreUnreachable},
		{"some brand new diagnostic nobody has seen", ErrCauseUndetermined},
	}
	for _, c := range cases {
		assert.ErrorIs(t, classifySecretStderr(c.diag), c.want, "diag %q", c.diag)
	}
}

func TestTagCause_IsMatchesThroughChain(t *testing.T) {
	err := tagCause(ErrNotAuthenticated, "1Password CLI op could not read 1pw://x/y/z: not signed in", "ref", "1pw://x/y/z")
	assert.True(t, stderrors.Is(err, ErrNotAuthenticated))
	assert.True(t, IsAuthFailure(err))
	assert.True(t, IsSecretFailure(err))
	assert.False(t, IsStoreUnreachable(err))
	assert.Contains(t, err.Error(), "not signed in")
}
