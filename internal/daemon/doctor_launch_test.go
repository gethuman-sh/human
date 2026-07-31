package daemon

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// An agent launched while the tracker is unreachable cannot read its plan and
// cannot post its handoff: it dies blaming the ticket for a credential that was
// unavailable for thirty seconds. The work must wait instead (SC-2173).
func TestLaunchRefusalChecks_holdsWorkWhenCredentialsAreUnavailable(t *testing.T) {
	assert.Contains(t, LaunchRefusalChecks(), "trackers")
}

// Everything that already stopped a launch still does.
func TestLaunchRefusalChecks_keepsTheCriticalSet(t *testing.T) {
	refusal := LaunchRefusalChecks()
	for _, id := range LaunchCriticalChecks {
		assert.Contains(t, refusal, id)
	}
}

// But a tracker blip must NOT be reported as gating: a momentary credential
// lapse raising a system alarm is the SC-1991 failure, and holding a launch is
// a quieter thing than declaring the substrate broken.
func TestLaunchCriticalChecks_trackerBlipRaisesNoAlarm(t *testing.T) {
	assert.False(t, slices.Contains(LaunchCriticalChecks, "trackers"),
		"gating is derived from this list; adding trackers here would alarm on a blip")
}

// The union must not mutate the list it is derived from — a caller appending to
// the returned slice must not quietly extend the alarm set.
func TestLaunchRefusalChecks_doesNotAliasTheCriticalSet(t *testing.T) {
	before := slices.Clone(LaunchCriticalChecks)
	got := LaunchRefusalChecks()
	got[0] = "mutated"

	assert.Equal(t, before, LaunchCriticalChecks)
}
