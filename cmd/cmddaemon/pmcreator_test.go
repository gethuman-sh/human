package cmddaemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/vault"
)

// writeHumanConfig drops a .humanconfig.yaml into a temp dir so resolvePMCreator
// exercises real config loading; a literal token is enough for Build to accept
// the entry (Build only skips when Token == "").
func writeHumanConfig(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(yaml), 0o644))
	return dir
}

// SC-1959: a board configured to show ALL work (projects empty) must still file
// new tickets into a dedicated place via create_in.
func TestResolvePMCreator_UsesCreateInOverProjects(t *testing.T) {
	dir := writeHumanConfig(t, `shortcuts:
  - name: board
    token: t0ken
    role: pm
    projects: []
    create_in: team-a
`)
	_, target, err := resolvePMCreator(dir, nil, vault.NewResolver())
	require.NoError(t, err)
	assert.Equal(t, "team-a", target)
}

// SC-1959: neither projects nor create_in means the ticket would file
// group-less and land off every board — the creator must refuse loudly.
func TestResolvePMCreator_EmptyFilingTargetErrors(t *testing.T) {
	dir := writeHumanConfig(t, `shortcuts:
  - name: board
    token: t0ken
    role: pm
`)
	_, _, err := resolvePMCreator(dir, nil, vault.NewResolver())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "filing target")
}

// SC-1959 (AC #4): legacy single-knob configs keep filing into projects[0]
// with no reconfiguration.
func TestResolvePMCreator_ProjectsOnlyStillWorks(t *testing.T) {
	dir := writeHumanConfig(t, `shortcuts:
  - name: board
    token: t0ken
    role: pm
    projects: [team-a, team-b]
`)
	_, target, err := resolvePMCreator(dir, nil, vault.NewResolver())
	require.NoError(t, err)
	assert.Equal(t, "team-a", target)
}
