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

// Two undeclared trackers are ambiguous whatever they are. A Shortcut among them
// used to win by kind, which is exactly the privilege this rule no longer grants:
// the board says it cannot tell rather than picking one and being quietly wrong.
func TestResolveTopology_singleWithoutEngineeringRole(t *testing.T) {
	instances := []Instance{
		{Name: "board", Kind: "shortcut"},
		{Name: "issues", Kind: "linear"},
	}
	top := ResolveTopology(instances)
	assert.Equal(t, "single", top.Mode)
	assert.Nil(t, top.PM, "neither declared pm, and neither kind earns it")
	assert.Nil(t, top.Engineering)
}

// Declaring resolves it, on any backend — which is the whole remedy the board
// notice points at.
func TestResolveTopology_declaredPMWinsOnAnyKind(t *testing.T) {
	for _, kind := range []string{"shortcut", "jira", "linear", "github", "gitlab", "azuredevops", "clickup"} {
		t.Run(kind, func(t *testing.T) {
			top := ResolveTopology([]Instance{
				{Name: "board", Kind: kind, Role: "pm"},
				{Name: "other", Kind: "linear"},
			})
			require.NotNil(t, top.PM)
			assert.Equal(t, "board", top.PM.Name)
		})
	}
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
		{Name: "pm1", Kind: "shortcut", Role: "pm"},
		{Name: "pm2", Kind: "shortcut", Role: "pm"},
		{Name: "eng1", Kind: "linear", Role: "engineering"},
		{Name: "eng2", Kind: "github", Role: "engineering"},
	}
	top := ResolveTopology(instances)
	require.NotNil(t, top.PM)
	require.NotNil(t, top.Engineering)
	assert.Equal(t, "pm1", top.PM.Name)
	assert.Equal(t, "eng1", top.Engineering.Name)
}

// The SC-1671 defect was a forge counted as a second PM candidate, which left a
// lone real tracker resolving to nil. It cannot recur through this path: a forge
// is not a tracker.Instance, so a sole tracker is sole whatever code hosts are
// configured beside it ([SC-3876]).
func TestResolveTopology_soleTrackerResolvesWhateverElseIsConfigured(t *testing.T) {
	top := ResolveTopology([]Instance{{Name: "work", Kind: "linear", Provider: stubProvider{}}})
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
}
