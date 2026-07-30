package cmdutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/vault"
)

// failingProvider stands in for a vault whose backing CLI is momentarily
// unavailable — the real-world trigger — without shelling out to it.
type failingProvider struct{}

func (failingProvider) CanResolve(ref string) bool { return strings.HasPrefix(ref, "1pw://") }

func (failingProvider) Resolve(string) (string, error) {
	return "", errors.New("resolving 1Password secret via CLI")
}

// configDir writes a .humanconfig.yaml into a fresh temp dir.
func configDir(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(yaml), 0o600))
	return dir
}

// oneBadOneGood configures two trackers where only the first carries an
// unresolvable secret reference.
const oneBadOneGood = `
vault:
  provider: 1password
  account: nonexistent-account-for-test
githubs:
  - name: broken
    token: 1pw://NoSuchVault/NoSuchItem/token
shortcuts:
  - name: working
    token: plain-token-value
`

// The defect: one provider's credential failure erased every OTHER provider's
// instances, so a momentary vault hiccup blanked the whole board (SC-2005).
func TestLoadAllInstancesTolerant_KeepsTheTrackersThatLoaded(t *testing.T) {
	dir := configDir(t, oneBadOneGood)

	instances, failures := LoadAllInstancesTolerant(dir, nil, vault.NewResolver(failingProvider{}))

	require.NotEmpty(t, failures, "the unresolvable reference must be reported")
	names := make([]string, 0, len(instances))
	for _, inst := range instances {
		names = append(names, inst.Name)
	}
	assert.Contains(t, names, "working",
		"a working tracker must survive another tracker's credential failure")
	assert.NotContains(t, names, "broken")
}

// The strict variant keeps its all-or-nothing contract: callers that need one
// specific tracker must still be told the load failed rather than handed a
// silently short list.
func TestLoadAllInstancesWithResolver_StillFailsWholesale(t *testing.T) {
	dir := configDir(t, oneBadOneGood)

	instances, err := LoadAllInstancesWithResolver(dir, nil, vault.NewResolver(failingProvider{}))

	require.Error(t, err, "the strict loader must keep aborting on a credential failure")
	assert.Nil(t, instances)
}

// With every credential resolvable there is nothing to report, and the tolerant
// loader must behave exactly like the strict one.
func TestLoadAllInstancesTolerant_NoFailuresWhenAllLoad(t *testing.T) {
	dir := configDir(t, `
shortcuts:
  - name: working
    token: plain-token-value
`)

	instances, failures := LoadAllInstancesTolerant(dir, nil, vault.NewResolver())

	assert.Empty(t, failures)
	require.Len(t, instances, 1)
	assert.Equal(t, "working", instances[0].Name)
}
