package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStampDaemon_appendsLine(t *testing.T) {
	got := StampDaemon("[human:review-started]", "ab12cd34")
	assert.Equal(t, "[human:review-started]\ndaemon: ab12cd34", got)
}

func TestStampDaemon_appendsAfterExistingLines(t *testing.T) {
	got := StampDaemon("[human:ready-for-review]\nbranch: main\ncommits: 2037e40", "d1")
	assert.Equal(t, "[human:ready-for-review]\nbranch: main\ncommits: 2037e40\ndaemon: d1", got)
}

func TestStampDaemon_emptyIDNoOp(t *testing.T) {
	body := "[human:review-started]"
	assert.Equal(t, body, StampDaemon(body, ""))
	assert.Equal(t, body, StampDaemon(body, "   "))
}

func TestParseDaemonID_roundTrip(t *testing.T) {
	body := StampDaemon("[human:ready-for-review]\nbranch: main", "ab12cd34")
	assert.Equal(t, "ab12cd34", ParseDaemonID(body))
}

func TestParseDaemonID_absent(t *testing.T) {
	assert.Equal(t, "", ParseDaemonID("[human:ready-for-review]\nbranch: main"))
}

// The daemon: line is appended AFTER a marker's content lines; it must be
// invisible to every existing parser, mirroring the pr:/branch:/commits:
// convention.
func TestClassifyMarker_ignoresDaemonLine(t *testing.T) {
	unstamped := "[human:ready-for-review]\nbranch: main"
	stamped := StampDaemon(unstamped, "x1")

	s1, st1, ok1 := ClassifyMarker(unstamped)
	s2, st2, ok2 := ClassifyMarker(stamped)

	assert.Equal(t, ok1, ok2)
	assert.Equal(t, s1, s2)
	assert.Equal(t, st1, st2)
}

func TestParseEngineeringKeys_ignoresDaemonLine(t *testing.T) {
	unstamped := "[human:ready-for-review]\nengineering: HUM-7, HUM-8\nbranch: main"
	stamped := StampDaemon(unstamped, "x1")

	assert.Equal(t,
		ParseEngineeringKeysFromHandoff(unstamped),
		ParseEngineeringKeysFromHandoff(stamped),
	)
}

func TestParsePR_ignoresDaemonLine(t *testing.T) {
	unstamped := "[human:ready-for-review]\nbranch: main\npr: https://example/pr/1"
	stamped := StampDaemon(unstamped, "x1")

	assert.Equal(t,
		ParsePRFromHandoff(unstamped),
		ParsePRFromHandoff(stamped),
	)
}
