package cmddaemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
)

// findCheck returns the definition with the given id.
func findCheck(t *testing.T, defs []daemon.DoctorCheckDef, id string) daemon.DoctorCheckDef {
	t.Helper()
	for _, d := range defs {
		if d.ID == id {
			return d
		}
	}
	require.FailNowf(t, "check not found", "expected a %q check", id)
	return daemon.DoctorCheckDef{}
}

// The tracker check holds work without alarming: an agent launched while the
// tracker is unreachable spends its run rediscovering that, but a thirty-second
// credential lapse is not a broken substrate (SC-1991 vs SC-2173).
func TestDoctorChecks_trackerLapseHoldsWorkWithoutAlarming(t *testing.T) {
	trackers := findCheck(t, buildDoctorChecks(nil, nil, doctorPersistence{}), "trackers")

	assert.True(t, trackers.Holding, "work must not launch into an unreachable tracker")
	assert.False(t, trackers.Gating, "a blip must not read as a broken substrate")
}

// A genuinely broken substrate still gates, so the quieter state did not
// swallow the loud one.
func TestDoctorChecks_brokenSubstrateStillGates(t *testing.T) {
	docker := findCheck(t, buildDoctorChecks(nil, nil, doctorPersistence{}), "docker")

	assert.True(t, docker.Gating)
	assert.False(t, docker.Holding)
}
