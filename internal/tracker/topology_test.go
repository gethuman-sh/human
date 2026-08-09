package tracker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTopology_declaredEngineeringResolved(t *testing.T) {
	declared := []TrackerStatus{{Name: "eng", Kind: "linear", Role: "engineering"}}
	assert.NoError(t, ValidateTopology(declared, true))
}

func TestValidateTopology_declaredEngineeringUnresolved(t *testing.T) {
	declared := []TrackerStatus{{Name: "eng", Kind: "linear", Role: "engineering"}}
	err := ValidateTopology(declared, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares an engineering-role tracker")
}

func TestValidateTopology_noEngineeringDeclared(t *testing.T) {
	declared := []TrackerStatus{{Name: "board", Kind: "shortcut", Role: "pm"}}
	assert.NoError(t, ValidateTopology(declared, false))
}

func TestDiagnoseTrackers_capturesRole(t *testing.T) {
	unmarshal := func(_, section string, target any) error {
		if section == "linears" {
			entries := target.(*[]diagnoseEntry)
			*entries = []diagnoseEntry{{Name: "eng", Role: "engineering"}}
		}
		return nil
	}
	getenv := func(_ string) string { return "" }

	statuses := DiagnoseTrackers(".", unmarshal, getenv)

	var found *TrackerStatus
	for i := range statuses {
		if statuses[i].Name == "eng" && statuses[i].Kind == "linear" {
			found = &statuses[i]
			break
		}
	}
	require.NotNil(t, found, "expected linear/eng in results")
	assert.Equal(t, "engineering", found.Role)
}

func TestResolveTopology_splitOnExplicitEngineering(t *testing.T) {
	instances := []Instance{
		{Name: "board", Kind: "shortcut"},
		{Name: "eng", Kind: "linear", Role: "engineering"},
	}
	top := ResolveTopology(instances)
	assert.Equal(t, "split", top.Mode)
	require.NotNil(t, top.PM)
	assert.Equal(t, "board", top.PM.Name)
	require.NotNil(t, top.Engineering)
	assert.Equal(t, "eng", top.Engineering.Name)
}

func TestResolveTopology_singleWithoutEngineeringRole(t *testing.T) {
	instances := []Instance{
		{Name: "board", Kind: "shortcut"},
		{Name: "issues", Kind: "linear"},
	}
	top := ResolveTopology(instances)
	assert.Equal(t, "single", top.Mode)
	require.NotNil(t, top.PM)
	assert.Equal(t, "board", top.PM.Name)
	assert.Nil(t, top.Engineering)
}

func TestResolveTopology_pmFallbackToSoleTracker(t *testing.T) {
	instances := []Instance{{Name: "only", Kind: "jira"}}
	top := ResolveTopology(instances)
	assert.Equal(t, "single", top.Mode)
	require.NotNil(t, top.PM)
	assert.Equal(t, "only", top.PM.Name)
}

func TestResolveTopology_pmAmbiguousStaysNil(t *testing.T) {
	instances := []Instance{
		{Name: "a", Kind: "jira"},
		{Name: "b", Kind: "linear"},
	}
	top := ResolveTopology(instances)
	assert.Equal(t, "single", top.Mode)
	assert.Nil(t, top.PM)
}

func TestResolveTopology_firstRoleWins(t *testing.T) {
	instances := []Instance{
		{Name: "pm1", Kind: "shortcut"},
		{Name: "pm2", Kind: "shortcut"},
		{Name: "eng1", Kind: "linear", Role: "engineering"},
		{Name: "eng2", Kind: "github", Role: "engineering"},
	}
	top := ResolveTopology(instances)
	require.NotNil(t, top.PM)
	require.NotNil(t, top.Engineering)
	assert.Equal(t, "pm1", top.PM.Name)
	assert.Equal(t, "eng1", top.Engineering.Name)
}

// TestResolveTopology_ignoresForgeOnly is the SC-1671 regression: a forge-only
// entry (role: forge) must never count as a second PM candidate, or a lone
// real tracker beside it makes PM resolution ambiguous and stays nil.
func TestResolveTopology_ignoresForgeOnly(t *testing.T) {
	instances := []Instance{
		{Name: "work", Kind: "linear", Provider: stubProvider{}},
		forgeOnly("prs"),
	}
	top := ResolveTopology(instances)
	assert.Equal(t, "single", top.Mode)
	require.NotNil(t, top.PM)
	assert.Equal(t, "work", top.PM.Name)
	assert.Nil(t, top.Engineering)
}

func TestResolveTopology_empty(t *testing.T) {
	top := ResolveTopology(nil)
	assert.Equal(t, "single", top.Mode)
	assert.Nil(t, top.PM)
	assert.Nil(t, top.Engineering)
	assert.Empty(t, top.Forges)
	assert.Empty(t, top.Notes, "an empty config has nothing to explain")
}

// dualGitHub is the shape a bare githubs: entry builds: no role declared, so it
// carries a tracker Provider AND a Forge at once.
func dualGitHub(name string) Instance {
	return Instance{Name: name, Kind: "github", Provider: stubProvider{}, Forge: stubForge{}}
}

// A forge is reported by the one view that can report it. `human tracker list`
// hides forge entries so an agent cannot pick one as a write target ([SC-1671]),
// which left them visible nowhere at all.
func TestResolveTopology_reportsForgeEntries(t *testing.T) {
	top := ResolveTopology([]Instance{
		{Name: "board", Kind: "shortcut", Provider: stubProvider{}},
		forgeOnly("prs"),
	})

	require.Len(t, top.Forges, 1)
	assert.Equal(t, "prs", top.Forges[0].Name)
	assert.Nil(t, top.Forges[0].Provider, "a declared forge carries no tracker capability")
	assert.Empty(t, top.Notes, "a config that declared its intent has nothing to explain")
}

// The ambiguity that made the same misreading a bug three times over: a GitHub
// entry with no role is both things at once, and nothing in the config says so.
func TestResolveTopology_notesTheUndeclaredGitHubEntry(t *testing.T) {
	top := ResolveTopology([]Instance{
		{Name: "board", Kind: "shortcut", Provider: stubProvider{}},
		dualGitHub("human"),
	})

	require.Len(t, top.Notes, 1)
	assert.Contains(t, top.Notes[0], `github "human"`)
	assert.Contains(t, top.Notes[0], "declares no role")
	assert.Contains(t, top.Notes[0], "never ask it for tickets",
		"the note must say what the silence COSTS, not merely that it is ambiguous")

	require.Len(t, top.Forges, 1, "it opens pull requests, so it is a forge")
	assert.NotNil(t, top.Forges[0].Provider, "and it is still a tracker — that is the whole ambiguity")
}

// The quieter half of the same gap: declaring role: pm on the only GitHub entry
// takes the forge away with it, and nothing reports that until a pipeline
// reaches the PR step and stops.
func TestResolveTopology_notesAConfigWithNoForge(t *testing.T) {
	top := ResolveTopology([]Instance{
		{Name: "board", Kind: "shortcut", Provider: stubProvider{}},
		{Name: "work", Kind: "github", Role: "pm", Provider: stubProvider{}},
	})

	require.Len(t, top.Notes, 1)
	assert.Contains(t, top.Notes[0], "No forge configured")
	assert.Contains(t, top.Notes[0], "human pr create")
	assert.Empty(t, top.Forges)
}

// Reporting the forge must not disturb what the command already answered.
func TestResolveTopology_undeclaredGitHubStillResolvesAsATracker(t *testing.T) {
	top := ResolveTopology([]Instance{dualGitHub("human")})

	assert.Equal(t, "single", top.Mode)
	require.NotNil(t, top.PM, "a lone roleless GitHub entry is still the sole PM candidate")
	assert.Equal(t, "human", top.PM.Name)
}
