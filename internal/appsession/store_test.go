package appsession

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "appsession.json"))
}

func TestStore_MarkReadClear_RoundTrip(t *testing.T) {
	s := newTestStore(t)

	_, present := s.Read()
	assert.False(t, present, "no marker written yet")

	require.NoError(t, s.Mark(111, 222))
	marker, present := s.Read()
	require.True(t, present)
	assert.Equal(t, 111, marker.AppPID)
	assert.Equal(t, 222, marker.DaemonPID)

	require.NoError(t, s.Clear())
	_, present = s.Read()
	assert.False(t, present, "cleared marker must read as absent")
}

func TestStore_Clear_NoFileIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Clear())
}

func alwaysAlive(int) bool { return true }
func neverAlive(int) bool  { return false }

func TestIsOrphaned(t *testing.T) {
	cases := []struct {
		name     string
		marker   Marker
		present  bool
		daemonID int
		alive    func(int) bool
		want     bool
	}{
		{"no marker at all", Marker{}, false, 222, neverAlive, false},
		{"same daemon, recording app dead", Marker{AppPID: 111, DaemonPID: 222}, true, 222, neverAlive, true},
		{"same daemon, recording app still alive", Marker{AppPID: 111, DaemonPID: 222}, true, 222, alwaysAlive, false},
		{"daemon restarted since (different PID)", Marker{AppPID: 111, DaemonPID: 222}, true, 999, neverAlive, false},
		{"marker with zero daemon PID", Marker{AppPID: 111, DaemonPID: 0}, true, 0, neverAlive, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsOrphaned(c.marker, c.present, c.daemonID, c.alive)
			assert.Equal(t, c.want, got)
		})
	}
}
