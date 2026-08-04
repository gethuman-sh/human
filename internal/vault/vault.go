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

// DefaultCacheTTL bounds how long an *untouched* resolved secret is kept in
// memory as a lapse fallback. It is a sliding idle window: every read refreshes
// it, so what retires an entry is idleness rather than age (SC-3321). It covers
// a typical board run (secrets read at push time must still resolve at deploy
// time, in a separate daemon request) without letting plaintext linger
// indefinitely. Operators tighten or disable it via the vault `cache_ttl`
// config field (a zero or negative TTL disables caching entirely, restoring the
// strict no-persistence behaviour).
const DefaultCacheTTL = 15 * time.Minute

// DefaultMaxCacheTTL is the absolute ceiling on how long a resolved secret may
// stay in memory, no matter how continuously it is used. The idle window
// (cache_ttl) slides forward on every hit so an in-use secret never forces
// re-approval (SC-3321); this bound is what stops "in continuous use" from
// meaning "held forever", capping how long sealed plaintext lingers (SC-2183).
// It comfortably covers a single unattended overnight run while remaining a
// hard, day-bounded ceiling; operators tune it via the vault `cache_max_ttl`
// field.
const DefaultMaxCacheTTL = 24 * time.Hour

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
// A resolved secret is served from memory for a bounded TTL, and the provider
// is consulted only on a miss. That is what keeps a human out of the machine's
// loop: an interactive store (the 1Password desktop app) raises an approval
// dialog per read, so reaching the provider on every request means approving
// one prompt per command the pipeline runs — faster than anyone can clear
// them, and a missed one fails an unrelated piece of work half a minute later
// (SC-2173).
//
// The window slides: each read pushes the entry's expiry out by another
// `cache_ttl`, so a secret the pipeline is actively using never forces a fresh
// approval, while one nobody asks for is retired a window after its last use —
// idleness, not age, is what retires it. An absolute `cache_max_ttl` ceiling
// caps that sliding so continuous use never means held forever (SC-3321).
//
// The cost is the honest one for any cache: a rotated secret is picked up when
// the entry ages out rather than instantly. Both windows are configurable, and a
// non-positive `cache_ttl` disables caching entirely for anyone who wants the
// provider consulted every time.
//
// Concurrent resolutions of the same reference are collapsed into one provider
// call, so several pieces of work starting together raise one approval between
// them instead of one each.
//
// A still-fresh entry is also served when every claiming provider fails — a
// credential lapse. This keeps a momentary loss of access from invalidating a
// secret that already resolved in the same run (SC-2039). The cache lives only
// in daemon memory, never on disk. Every entry is TTL-bounded: remember sweeps
// expired entries on each successful resolve (so a ref that is never
// re-requested does not linger past its TTL just because nobody asked for it
// again), and cached additionally drops its own entry on a stale read.
type Resolver struct {
	providers []SecretProvider

	// ttl is the sliding idle window: an untouched entry expires after this,
	// and every cache hit refreshes it (SC-3321).
	ttl time.Duration
	// maxTTL is the absolute lifetime ceiling; the sliding window may never
	// push an entry's expiry past its creation + maxTTL (SC-2183).
	maxTTL time.Duration
	now    func() time.Time
	mu     sync.Mutex
	cache  map[string]cachedSecret
	// inflight holds the resolution currently running for a reference, so
	// callers that arrive while it runs wait for its result instead of raising
	// a second approval prompt for the same secret.
	inflight map[string]*resolveCall
}

// resolveCall is one in-progress resolution, shared by everyone who asked for
// the same reference while it was running.
type resolveCall struct {
	done chan struct{}
	val  string
	err  error
}

// cachedSecret is a resolved secret with the instant it stops being valid.
// The value is sealed rather than held as a string: it is the only plaintext
// that outlives a single call, so it is the copy a core file or a swap page
// would capture (SC-2183).
type cachedSecret struct {
	value sealed
	// expiresAt is the sliding idle deadline, refreshed on each hit.
	expiresAt time.Time
	// deadline is the fixed absolute ceiling set at creation; the idle
	// deadline is never slid past it (SC-2183).
	deadline time.Time
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
		maxTTL:    DefaultMaxCacheTTL,
		now:       time.Now,
		cache:     make(map[string]cachedSecret),
		inflight:  make(map[string]*resolveCall),
	}
}

// Resolve looks up a secret reference. If the value is not a vault reference
// (no provider claims it), the original value is returned unchanged.
//
// A still-fresh cached value is served without touching the provider. With an
// interactive store that is the difference between one approval prompt and one
// per command the pipeline runs.
func (r *Resolver) Resolve(ref string) (string, error) {
	if !IsSecretRef(ref) {
		return ref, nil
	}
	if val, ok := r.cached(ref); ok {
		return val, nil
	}
	return r.resolveOnce(ref)
}

// resolveOnce consults the providers, with concurrent callers asking for the
// same reference sharing one call.
//
// Without this, ten pieces of work starting together raise ten prompts for one
// secret — and a person cannot clear ten dialogs before the first times out, so
// a burst of activity is precisely when resolution is most likely to fail.
func (r *Resolver) resolveOnce(ref string) (string, error) {
	r.mu.Lock()
	if r.inflight == nil {
		r.inflight = make(map[string]*resolveCall)
	}
	if call, running := r.inflight[ref]; running {
		r.mu.Unlock()
		<-call.done
		return call.val, call.err
	}
	call := &resolveCall{done: make(chan struct{})}
	r.inflight[ref] = call
	r.mu.Unlock()

	// Secret mode bounds how long the working copies made while reading the
	// secret stay legible in this process — the bytes from the store, the
	// trimming, the parsing. Without it they are ordinary garbage, readable
	// until something happens to overwrite them (SC-2183).
	eraseTemporaries(func() {
		call.val, call.err = r.fromProviders(ref)
	})

	r.mu.Lock()
	delete(r.inflight, ref)
	r.mu.Unlock()
	close(call.done)
	return call.val, call.err
}

// fromProviders tries each claiming provider in order and remembers what one of
// them returns. Providers are tried in order; the first claimant that succeeds
// wins.
func (r *Resolver) fromProviders(ref string) (string, error) {
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
		// the lapse surface as the error it is. Reached when an entry landed
		// while this call was running, or expired between the read above and
		// the failure here.
		if val, ok := r.cached(ref); ok {
			return val, nil
		}
		return "", lastErr
	}

	// No provider claims this reference — return as-is.
	return ref, nil
}

// remember stores a freshly resolved value as a lapse fallback, with two
// deadlines: the sliding idle window that reads refresh, and the fixed absolute
// ceiling the sliding may never pass.
// A non-positive TTL disables caching so no plaintext persists. It
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
	now := r.now()
	deadline := now.Add(r.effectiveMaxTTLLocked())
	expiresAt := now.Add(r.ttl)
	if expiresAt.After(deadline) {
		expiresAt = deadline
	}
	r.cache[ref] = cachedSecret{value: seal(val), expiresAt: expiresAt, deadline: deadline}
}

// effectiveMaxTTLLocked is the absolute ceiling clamped so it is never shorter
// than one idle window: a max below the idle window has no coherent meaning, so
// a config that sets only cache_ttl (with cache_ttl above the default max)
// degrades to a single-window lifetime rather than a contradiction.
// Callers must hold r.mu.
func (r *Resolver) effectiveMaxTTLLocked() time.Duration {
	if r.maxTTL < r.ttl {
		return r.ttl
	}
	return r.maxTTL
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

// cached returns a still-fresh cached value for ref, sliding its idle window
// forward on the way out. An expired entry is dropped on read so plaintext
// never outlives its window.
func (r *Resolver) cached(ref string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[ref]
	if !ok {
		return "", false
	}
	now := r.now()
	// Idle-expired, or past the absolute ceiling: drop it. The ceiling check is
	// defensive — expiresAt is always kept <= deadline — but it makes the
	// max-lifetime guard explicit and holds even if ttl is misconfigured above
	// maxTTL.
	if !now.Before(entry.expiresAt) || !now.Before(entry.deadline) {
		delete(r.cache, ref)
		return "", false
	}
	val, ok := entry.value.open()
	if !ok {
		// Unreadable is treated as absent: the caller goes to the provider,
		// which is right, instead of receiving an empty secret that would fail
		// somewhere less obvious.
		delete(r.cache, ref)
		return "", false
	}
	// Slide the idle window forward on use, but never past the absolute
	// deadline: an in-use secret does not force re-approval (SC-3321) yet still
	// cannot live forever (SC-2183).
	slid := now.Add(r.ttl)
	if slid.After(entry.deadline) {
		slid = entry.deadline
	}
	entry.expiresAt = slid
	r.cache[ref] = entry
	return val, true
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
