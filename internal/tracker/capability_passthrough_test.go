package tracker

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Providers are wrapped in a decorator chain (safe → policy → audit →
// destructive), and each wrapper re-declares the methods it forwards. An
// OPTIONAL capability — one callers reach by type assertion rather than through
// Provider — is therefore invisible unless every wrapper declares it too.
//
// This test exists because that failed silently. AssignToReporter was added to
// the Shortcut client, the wrappers knew nothing about it, and every call came
// back "tracker does not support assignment" — the decorated provider reporting
// a capability its inner provider had. Nothing failed to compile, because an
// optional capability is exactly the kind a type checker cannot miss you having.
//
// So: wrap a provider that HAS the capability in each decorator, and assert the
// decorator still offers it. Add an optional interface to optionalCapabilities
// when you add one, and every wrapper is checked against it from then on.
func TestWrappersPreserveOptionalCapabilities(t *testing.T) {
	inner := &fullyCapableProvider{}

	audit, err := NewAuditProvider(inner, "test", "shortcut", filepath.Join(t.TempDir(), "audit.log"))
	require.NoError(t, err)
	destructive, err := NewDestructiveProvider(inner, "test", "shortcut", filepath.Join(t.TempDir(), "destructive.log"), nil)
	require.NoError(t, err)

	wrappers := map[string]Provider{
		"SafeProvider":        NewSafeProvider(inner, "test"),
		"PolicyProvider":      NewPolicyProvider(inner, "test", &Policy{}, func(string) {}),
		"AuditProvider":       audit,
		"DestructiveProvider": destructive,
	}

	for name, w := range wrappers {
		t.Run(name, func(t *testing.T) {
			for capability, holds := range optionalCapabilities {
				assert.True(t, holds(w),
					"%s drops the %s capability its inner provider has — a caller type-asserting for it "+
						"sees a provider that cannot do something it can", name, capability)
			}
		})
	}
}

// optionalCapabilities are the interfaces callers reach by type assertion.
// Register a new one here and every wrapper is held to it.
var optionalCapabilities = map[string]func(any) bool{
	"Assigner":          func(p any) bool { _, ok := p.(Assigner); return ok },
	"CurrentUserGetter": func(p any) bool { _, ok := p.(CurrentUserGetter); return ok },
	"ReporterAssigner":  func(p any) bool { _, ok := p.(ReporterAssigner); return ok },
	"Transitioner":      func(p any) bool { _, ok := p.(Transitioner); return ok },
}

// Forwarding must actually reach the inner provider, not merely type-assert.
func TestWrappersForwardAssignToReporter(t *testing.T) {
	for name, wrap := range map[string]func(Provider) Provider{
		"SafeProvider":   func(p Provider) Provider { return NewSafeProvider(p, "test") },
		"PolicyProvider": func(p Provider) Provider { return NewPolicyProvider(p, "test", &Policy{}, func(string) {}) },
	} {
		t.Run(name, func(t *testing.T) {
			inner := &fullyCapableProvider{}
			assigner, ok := wrap(inner).(ReporterAssigner)
			require.True(t, ok)

			require.NoError(t, assigner.AssignToReporter(context.Background(), "SC-1"))
			assert.Equal(t, []string{"SC-1"}, inner.reporterAssigned, "the call must reach the inner provider")
		})
	}
}

// fullyCapableProvider implements Provider plus every optional capability, so a
// wrapper that drops one is the only thing a test can be measuring.
type fullyCapableProvider struct {
	stubProvider
	reporterAssigned []string
}

func (f *fullyCapableProvider) AssignToReporter(_ context.Context, key string) error {
	f.reporterAssigned = append(f.reporterAssigned, key)
	return nil
}
