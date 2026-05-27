//go:build cgo

package vault

import (
	"context"
	"strings"
	"sync"

	"github.com/1password/onepassword-sdk-go"

	"github.com/gethuman-sh/human/errors"
)

// secretRefPrefix is the user-facing prefix for 1Password secret references.
const secretRefPrefix = "1pw://"

// sdkRefPrefix is the prefix expected by the 1Password SDK internally.
const sdkRefPrefix = "op://"

// OnePassword resolves 1pw:// secret references using the 1Password Go SDK.
// It lazily initializes the SDK client on first use via the desktop app
// integration, which triggers biometric/master password authentication.
type OnePassword struct {
	// Account is the 1Password account name (shown top-left in the desktop app sidebar).
	Account string
	// IntegrationName identifies this integration to 1Password.
	IntegrationName string
	// IntegrationVersion identifies the version to 1Password.
	IntegrationVersion string

	// clientFactory overrides SDK client creation for testing.
	clientFactory func(ctx context.Context) (secretResolver, error)

	once    sync.Once
	client  secretResolver
	initErr error
}

// secretResolver abstracts the 1Password SDK secrets client for testing.
type secretResolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// NewOnePassword creates a 1Password provider using the SDK.
// The account parameter is the 1Password account name used for desktop app
// integration (biometric/master password authentication).
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

// Resolve uses the 1Password SDK to retrieve the secret value for the given reference.
// It translates the 1pw:// prefix to op:// before calling the SDK.
func (o *OnePassword) Resolve(ref string) (string, error) {
	o.once.Do(func() {
		o.client, o.initErr = o.createClient()
	})
	if o.initErr != nil {
		return "", errors.WrapWithDetails(o.initErr, "initializing 1Password SDK")
	}

	// Translate 1pw:// → op:// for the SDK.
	sdkRef := sdkRefPrefix + strings.TrimPrefix(ref, secretRefPrefix)

	val, err := o.client.Resolve(context.Background(), sdkRef)
	if err != nil {
		return "", errors.WrapWithDetails(err, "resolving 1Password secret", "ref", ref)
	}
	return val, nil
}

// createClient initializes the 1Password SDK client using desktop app integration.
func (o *OnePassword) createClient() (secretResolver, error) {
	if o.clientFactory != nil {
		return o.clientFactory(context.Background())
	}

	client, err := onepassword.NewClient(context.Background(),
		onepassword.WithDesktopAppIntegration(o.Account),
		onepassword.WithIntegrationInfo(o.IntegrationName, o.IntegrationVersion),
	)
	if err != nil {
		return nil, err
	}
	return client.Secrets(), nil
}
