package tracker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusFor finds one tracker entry in a diagnosis result.
func statusFor(t *testing.T, statuses []TrackerStatus, kind, name string) TrackerStatus {
	t.Helper()
	for _, st := range statuses {
		if st.Kind == kind && st.Name == name {
			return st
		}
	}
	require.FailNowf(t, "not found", "expected %s/%s in results", kind, name)
	return TrackerStatus{}
}

// diagnoseWithToken runs a diagnosis for a single entry in one config section.
func diagnoseWithToken(section, name, token string) []TrackerStatus {
	unmarshal := func(_, sec string, target any) error {
		if sec == section {
			entries := target.(*[]diagnoseEntry)
			*entries = []diagnoseEntry{{Name: name, Token: token}}
		}
		return nil
	}
	return DiagnoseTrackers(".", unmarshal, func(string) string { return "" })
}

// The defect: the reference test was hardcoded to "1pw://", so a gh:// token —
// a POINTER at the GitHub CLI's keyring, not a secret — fell through as a
// present literal value and the entry was reported as verified working. That is
// a false all-clear: the health report said the tracker was fine while nothing
// had resolved it (SC-1973).
func TestDiagnoseTrackers_ghRefIsAnUnverifiedReference(t *testing.T) {
	st := statusFor(t, diagnoseWithToken("githubs", "personal", "gh://token"), "github", "personal")

	assert.True(t, st.VaultRef, "a gh:// token is a reference, not a resolved secret")
	assert.False(t, st.Working, "nothing resolved it, so it must not be reported as verified")
	assert.Empty(t, st.Missing, "the value is present — it is unverified, not absent")
}

// The host-qualified form must classify the same way: the rule is the scheme,
// not one exact string.
func TestDiagnoseTrackers_ghRefWithHostIsAlsoAReference(t *testing.T) {
	st := statusFor(t, diagnoseWithToken("githubs", "enterprise", "gh://ghe.corp.com/token"), "github", "enterprise")

	assert.True(t, st.VaultRef)
	assert.False(t, st.Working)
}

// The scheme that already worked must keep working.
func TestDiagnoseTrackers_onePasswordRefStillClassifies(t *testing.T) {
	st := statusFor(t, diagnoseWithToken("shortcuts", "human", "1pw://Private/Shortcut Token/notesPlain"), "shortcut", "human")

	assert.True(t, st.VaultRef)
	assert.False(t, st.Working)
}

// A literal secret is NOT a reference: it is present and verified, and must not
// be demoted to unverified by a rule that over-matches.
func TestDiagnoseTrackers_literalTokenIsVerified(t *testing.T) {
	st := statusFor(t, diagnoseWithToken("githubs", "personal", "ghp_literalvalue"), "github", "personal")

	assert.False(t, st.VaultRef)
	assert.True(t, st.Working)
	assert.Empty(t, st.Missing)
}
