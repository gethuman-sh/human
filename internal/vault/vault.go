// Package vault resolves secret references from external vault providers.
// Config fields like "1pw://DevVault/GitHub PAT/token" are transparently
// resolved to their plaintext values at startup, avoiding the need for
// pre-populated environment variables.
package vault

import (
	"strings"
)

// SecretProvider resolves a secret reference to its plaintext value.
// Implementations must be safe for concurrent use.
type SecretProvider interface {
	// Resolve returns the plaintext value for the given reference.
	// The reference format is provider-specific (e.g. "1pw://vault/item/field").
	Resolve(ref string) (string, error)

	// CanResolve reports whether this provider handles the given reference.
	CanResolve(ref string) bool
}

// Resolver coordinates multiple SecretProviders.
// It is created once at daemon startup and injected into per-request
// command contexts via WithResolver so all requests share one provider
// instance (avoiding repeated op.exe subprocesses on WSL2).
// Secrets are resolved on every call — no caching — so plaintext values
// do not persist in daemon memory.
type Resolver struct {
	providers []SecretProvider
}

// NewResolver creates a Resolver with the given providers.
// Providers are tried in order; the first whose CanResolve returns true wins.
func NewResolver(providers ...SecretProvider) *Resolver {
	return &Resolver{
		providers: providers,
	}
}

// Resolve looks up a secret reference. If the value is not a vault reference
// (no provider claims it), the original value is returned unchanged.
func (r *Resolver) Resolve(ref string) (string, error) {
	if !IsSecretRef(ref) {
		return ref, nil
	}

	for _, p := range r.providers {
		if !p.CanResolve(ref) {
			continue
		}
		return p.Resolve(ref)
	}

	// No provider claims this reference — return as-is.
	return ref, nil
}

// IsSecretRef reports whether s looks like a vault secret reference.
// Currently recognizes "1pw://" (1Password) and "gh://" (GitHub CLI).
func IsSecretRef(s string) bool {
	return strings.HasPrefix(s, "1pw://") || strings.HasPrefix(s, ghRefPrefix)
}

// ResolveField resolves a single config field value through the vault.
// If the resolver is nil, the original value is returned unchanged.
func ResolveField(r *Resolver, value string) (string, error) {
	if r == nil {
		return value, nil
	}
	return r.Resolve(value)
}
