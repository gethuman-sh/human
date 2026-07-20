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

func TestResolveTopology_empty(t *testing.T) {
	top := ResolveTopology(nil)
	assert.Equal(t, "single", top.Mode)
	assert.Nil(t, top.PM)
	assert.Nil(t, top.Engineering)
}
