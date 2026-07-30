package vault

import (
	stderrors "errors"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// The secret-store failure causes. They are the machine-readable distinction
// callers match with errors.Is instead of parsing message text: a failed secret
// read must never be mistaken for a CI, branch, or forge failure (SC-2042).
var (
	// ErrNotAuthenticated: the store's CLI has no valid session (op not signed
	// in, gh not logged in). The remedy is to authenticate — nothing about the
	// branch, forge, or CI is wrong.
	ErrNotAuthenticated = stderrors.New("secret store: not authenticated")
	// ErrSecretMissing: the store was reachable and the session valid, but the
	// referenced vault/item/field does not exist.
	ErrSecretMissing = stderrors.New("secret store: secret not found")
	// ErrStoreUnreachable: the store's CLI could not be run or reached (binary
	// absent, no network, timed out).
	ErrStoreUnreachable = stderrors.New("secret store: unreachable")
	// ErrCauseUndetermined: the read failed but the diagnostic matched no known
	// cause. Surfaced explicitly so a caller still knows this was a SECRET
	// failure rather than falling back to blaming unrelated infrastructure.
	ErrCauseUndetermined = stderrors.New("secret store: read failed, cause undetermined")
)

// IsSecretFailure reports whether err is any classified secret-store failure.
func IsSecretFailure(err error) bool {
	return stderrors.Is(err, ErrNotAuthenticated) ||
		stderrors.Is(err, ErrSecretMissing) ||
		stderrors.Is(err, ErrStoreUnreachable) ||
		stderrors.Is(err, ErrCauseUndetermined)
}

// IsAuthFailure reports whether err is specifically a not-authenticated failure.
func IsAuthFailure(err error) bool { return stderrors.Is(err, ErrNotAuthenticated) }

// IsSecretMissing reports whether err is specifically a missing-secret failure.
func IsSecretMissing(err error) bool { return stderrors.Is(err, ErrSecretMissing) }

// IsStoreUnreachable reports whether err is specifically a store-unreachable failure.
func IsStoreUnreachable(err error) bool { return stderrors.Is(err, ErrStoreUnreachable) }

// tagCause wraps a provider failure's human message with a machine-readable
// cause sentinel. Only the sentinel stays in the Unwrap chain (the provider
// diagnostic already lives in message + details), so errors.Is(returned, cause)
// holds while errors.CauseChain still renders the full human message.
func tagCause(cause error, message string, details ...any) error {
	return errors.WrapWithDetails(cause, message, details...)
}

// classifySecretStderr maps a store CLI's stderr diagnostic to a cause sentinel.
// The keyword sets are the union of op and gh phrasings so a single classifier
// serves every provider. Ordered auth → missing → unreachable; anything
// unmatched is ErrCauseUndetermined so a miss degrades to an explicit
// "secret failure" rather than to a misclassification as CI/branch/forge.
func classifySecretStderr(diag string) error {
	d := strings.ToLower(diag)
	switch {
	case containsAny(d,
		"not signed in", "not currently signed in", "session expired",
		"no account", "authorization prompt", "unauthorized",
		"not logged in", "no accounts", "gh auth login", "authentication required"):
		return ErrNotAuthenticated
	case containsAny(d,
		"isn't an item", "isn't a vault", "no item", "item not found",
		"could not find", "doesn't have a field", "no field", "not found in",
		"doesn't exist", "no such vault", "no such item"):
		return ErrSecretMissing
	case containsAny(d,
		"could not connect", "couldn't connect", "connection refused",
		"dial tcp", "no such host", "network is unreachable", "i/o timeout",
		"timeout", "connecting to desktop app", "desktop app"):
		return ErrStoreUnreachable
	default:
		return ErrCauseUndetermined
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
