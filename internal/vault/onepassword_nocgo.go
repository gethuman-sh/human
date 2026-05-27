//go:build !cgo

package vault

import (
	"context"
	"strings"
	"sync"

	"github.com/gethuman-sh/human/errors"
)

// secretRefPrefix is the user-facing prefix for 1Password secret references.
const secretRefPrefix = "1pw://"

// sdkRefPrefix is the prefix expected by the 1Password SDK internally.
const sdkRefPrefix = "op://"

// OnePassword resolves 1pw:// secret references using the 1Password Go SDK.
// When built without CGO, CreateClient always returns an error directing
// users to the op CLI fallback.
type OnePassword struct {
	Account            string
	IntegrationName    string
	IntegrationVersion string

	clientFactory func(ctx context.Context) (secretResolver, error)

	once    sync.Once
	client  secretResolver
	initErr error
}

// secretResolver abstracts the 1Password SDK secrets client for testing.
type secretResolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// NewOnePassword creates a 1Password provider.
// Without CGO the SDK is unavailable; Resolve will return an error.
func NewOnePassword(account string) *OnePassword {
	return &OnePassword{
		Account:            account,
		IntegrationName:    "human-cli",
		IntegrationVersion: "1.0.0",
	}
}

// CanResolve reports whether ref is a 1Password reference (1pw:// prefix).
func (o *OnePassword) CanResolve(ref string) bool {
	return strings.HasPrefix(ref, secretRefPrefix)
}

// Resolve attempts to retrieve the secret. Without CGO this always fails
// unless a clientFactory is injected (tests).
func (o *OnePassword) Resolve(ref string) (string, error) {
	o.once.Do(func() {
		o.client, o.initErr = o.createClient()
	})
	if o.initErr != nil {
		return "", errors.WrapWithDetails(o.initErr, "initializing 1Password SDK")
	}

	sdkRef := sdkRefPrefix + strings.TrimPrefix(ref, secretRefPrefix)

	val, err := o.client.Resolve(context.Background(), sdkRef)
	if err != nil {
		return "", errors.WrapWithDetails(err, "resolving 1Password secret", "ref", ref)
	}
	return val, nil
}

func (o *OnePassword) createClient() (secretResolver, error) {
	if o.clientFactory != nil {
		return o.clientFactory(context.Background())
	}
	return nil, errors.WithDetails("1Password SDK requires CGO; use op CLI fallback (vault provider: 1password on WSL)")
}
