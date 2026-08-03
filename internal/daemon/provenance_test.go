package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/marker"
)

// ParseDaemonID reads the machine off a NEW signed marker (the machine: field
// the signing decorator injects into the field block).
func TestParseDaemonID_readsMachineField(t *testing.T) {
	body := marker.Sign("[human:ready-for-review]\nbranch: main", "ab12cd34", "rev+dirty")
	assert.Equal(t, "ab12cd34", ParseDaemonID(body))
}

// Legacy markers already on live tickets carry the id as a trailing daemon:
// line; the back-compat read path must still resolve them so claim arbitration
// survives the format change with no migration.
func TestParseDaemonID_readsLegacyDaemonLine(t *testing.T) {
	body := "[human:ready-for-review]\nbranch: main\ndaemon: legacy99"
	assert.Equal(t, "legacy99", ParseDaemonID(body))
}

func TestParseDaemonID_absent(t *testing.T) {
	assert.Equal(t, "", ParseDaemonID("[human:ready-for-review]\nbranch: main"))
}

// ParseBuild answers "which build wrote this record" off the signed build: field.
func TestParseBuild_readsBuildField(t *testing.T) {
	body := marker.Sign("[human:ready-for-review]\nbranch: main", "ab12cd34", "rev123+dirty")
	assert.Equal(t, "rev123+dirty", ParseBuild(body))
}

func TestParseBuild_absent(t *testing.T) {
	assert.Equal(t, "", ParseBuild("[human:ready-for-review]\nbranch: main\ndaemon: legacy99"))
}

// The signature is fields in the block, so it must not change how the marker
// classifies into a (stage, state).
func TestClassifyMarker_ignoresSignature(t *testing.T) {
	unsigned := "[human:ready-for-review]\nbranch: main"
	signed := marker.Sign(unsigned, "x1", "rev1")

	s1, st1, ok1 := ClassifyMarker(unsigned)
	s2, st2, ok2 := ClassifyMarker(signed)

	assert.Equal(t, ok1, ok2)
	assert.Equal(t, s1, s2)
	assert.Equal(t, st1, st2)
}

func TestParseEngineeringKeys_ignoresSignature(t *testing.T) {
	unsigned := "[human:ready-for-review]\nengineering: HUM-7, HUM-8\nbranch: main"
	signed := marker.Sign(unsigned, "x1", "rev1")

	assert.Equal(t,
		ParseEngineeringKeysFromHandoff(unsigned),
		ParseEngineeringKeysFromHandoff(signed),
	)
}

func TestParsePR_ignoresSignature(t *testing.T) {
	unsigned := "[human:ready-for-review]\nbranch: main\npr: https://example/pr/1"
	signed := marker.Sign(unsigned, "x1", "rev1")

	assert.Equal(t,
		ParsePRFromHandoff(unsigned),
		ParsePRFromHandoff(signed),
	)
}
