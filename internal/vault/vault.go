// Package vault resolves secret references from external vault providers.
// Config fields like "1pw://DevVault/GitHub PAT/token" are transparently
// resolved to their plaintext values at startup, avoiding the need for
// pre-populated environment variables.
package vault

import (
	"strings"
	"sync"
	"time"
)

// DefaultCacheTTL bounds how long a successfully resolved secret is kept in
// memory as a lapse fallback. It covers a typical board run (secrets read at
// push time must still resolve at deploy time, in a separate daemon request)
// without letting plaintext linger indefinitely. Operators tighten or disable
// it via the vault `cache_ttl` config field (a zero or negative TTL disables
// caching entirely, restoring the strict no-persistence behaviour).
const DefaultCacheTTL = 15 * time.Minute

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
//
// Providers are always tried first, so a rotated secret is picked up
// immediately while the store is reachable. A successful resolution is
// remembered for a bounded TTL, and only surfaces as a fallback when every
// claiming provider later fails — a credential lapse. This keeps a momentary
// loss of access from invalidating a secret that already resolved in the same
// run (SC-2039): the deploy step that reads a credential it read at push time
// survives the lapse instead of failing on an unrelated stale read. The cache
// lives only in daemon memory, never on disk. Every entry is TTL-bounded:
// remember sweeps expired entries on each successful resolve (so a ref that
// is never re-requested does not linger past its TTL just because nobody
// asked for it again), and cached additionally drops its own entry on a
// stale read.
type Resolver struct {
	providers []SecretProvider

	ttl   time.Duration
	now   func() time.Time
	mu    sync.Mutex
	cache map[string]cachedSecret
}

// cachedSecret is a resolved plaintext value with the instant it stops being a
// valid lapse fallback.
type cachedSecret struct {
	value     string
	expiresAt time.Time
}

// NewResolver creates a Resolver with the given providers and the default
// cache TTL. Providers are tried in order; the first claiming provider that
// succeeds wins. When a claimant errors, resolution falls through to the next
// claimant so a later provider (e.g. the op CLI behind the SDK) can still
// resolve the reference; if every claimant errors, a still-fresh cached value
// is used, and only when none exists does the last error surface.
func NewResolver(providers ...SecretProvider) *Resolver {
	return &Resolver{
		providers: providers,
		ttl:       DefaultCacheTTL,
		now:       time.Now,
		cache:     make(map[string]cachedSecret),
	}
}

// Resolve looks up a secret reference. If the value is not a vault reference
// (no provider claims it), the original value is returned unchanged.
func (r *Resolver) Resolve(ref string) (string, error) {
	if !IsSecretRef(ref) {
		return ref, nil
	}

	var lastErr error
	claimed := false
	for _, p := range r.providers {
		if !p.CanResolve(ref) {
			continue
		}
		claimed = true
		val, err := p.Resolve(ref)
		if err != nil {
			// Fall through: a later claimant (op CLI) may still resolve it.
			lastErr = err
			continue
		}
		r.remember(ref, val)
		return val, nil
	}

	if claimed {
		// Every claimant failed — a credential lapse. Serve a still-fresh
		// cached value so work whose secret already resolved this run is not
		// failed by an unrelated stale read; only when nothing is cached does
		// the lapse surface as the error it is.
		if val, ok := r.cached(ref); ok {
			return val, nil
		}
		return "", lastErr
	}

	// No provider claims this reference — return as-is.
	return ref, nil
}

// remember stores a freshly resolved value as a lapse fallback for the TTL
// window. A non-positive TTL disables caching so no plaintext persists. It
// also sweeps every expired entry from the cache, not just ref's: a secret
// that resolves once and is never requested again would otherwise never be
// evicted, since cached() only prunes the single key it is asked to read.
// Sweeping here means eviction rides on any subsequent successful resolve
// (of any reference), so plaintext does not outlive its TTL just because its
// own ref happens not to be re-requested.
func (r *Resolver) remember(ref, val string) {
	if r.ttl <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepExpiredLocked()
	r.cache[ref] = cachedSecret{value: val, expiresAt: r.now().Add(r.ttl)}
}

// sweepExpiredLocked deletes every cache entry whose TTL has passed. Callers
// must hold r.mu.
func (r *Resolver) sweepExpiredLocked() {
	now := r.now()
	for k, entry := range r.cache {
		if !now.Before(entry.expiresAt) {
			delete(r.cache, k)
		}
	}
}

// cached returns a still-fresh cached value for ref. An expired entry is
// dropped on read so plaintext never outlives its TTL window.
func (r *Resolver) cached(ref string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[ref]
	if !ok {
		return "", false
	}
	if !r.now().Before(entry.expiresAt) {
		delete(r.cache, ref)
		return "", false
	}
	return entry.value, true
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
