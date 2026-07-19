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
