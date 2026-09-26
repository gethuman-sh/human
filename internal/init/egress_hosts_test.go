package init

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryBootstrapCommandDeclaresItsEgressHosts refuses to let a stack gain
// an install command whose hosts nobody declared — the exact drift that left
// the shipped allowlist blocking the bootstrap it generated (SC-5879).
func TestEveryBootstrapCommandDeclaresItsEgressHosts(t *testing.T) {
	for _, cmd := range stackInstallCmd {
		hosts := hostsForCommand(cmd)
		assert.NotEmptyf(t, hosts, "command %q declares no egress hosts in bootstrapEgressHosts", cmd)
	}
	for _, p := range LspRegistry() {
		if p.InstallCmd == "" {
			continue
		}
		hosts := hostsForCommand(p.InstallCmd)
		assert.NotEmptyf(t, hosts, "LSP install command %q declares no egress hosts in bootstrapEgressHosts", p.InstallCmd)
	}
}

// TestProxyDomainsForStacks_KeepsDefaultsAndAddsStackHosts asserts the
// deterministic order: every default first, then the stacks' own hosts, no
// duplicates — so a regenerated config produces the same bytes.
func TestProxyDomainsForStacks_KeepsDefaultsAndAddsStackHosts(t *testing.T) {
	stacks := stacksByFeature(t,
		"ghcr.io/devcontainers/features/node:1",
		"ghcr.io/devcontainers/features/go:1")

	got := ProxyDomainsForStacks(stacks)

	require.GreaterOrEqual(t, len(got), len(DefaultProxyDomains))
	for i, d := range DefaultProxyDomains {
		require.Equalf(t, d, got[i], "default domain %d must stay in place", i)
	}
	assert.Contains(t, got, "proxy.golang.org")
	assert.Contains(t, got, "sum.golang.org")
	assert.Contains(t, got, "registry.npmjs.org")

	seen := make(map[string]bool)
	for _, d := range got {
		require.Falsef(t, seen[d], "domain %q listed more than once", d)
		seen[d] = true
	}
}

// TestProxyDomainsForStacks_NoStacksIsTheDefaultList is the baseline: a
// project with no selected stacks gets exactly the shipped defaults.
func TestProxyDomainsForStacks_NoStacksIsTheDefaultList(t *testing.T) {
	assert.Equal(t, DefaultProxyDomains, ProxyDomainsForStacks(nil))
}

// TestStackEgressHosts_GoStackWithoutInstallCommand: the toolchain switch needs
// the Go hosts even when no `go install …` line is ever written — it is what
// recovers an image whose Go is older than go.mod, with no command of its own.
func TestStackEgressHosts_GoStackWithoutInstallCommand(t *testing.T) {
	hosts := StackEgressHosts([]StackType{{Label: "Go", FeatureKey: goFeatureKey}})
	assert.Contains(t, hosts, "proxy.golang.org")
	assert.Contains(t, hosts, "sum.golang.org")
}
