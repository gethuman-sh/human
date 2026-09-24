package cmddoctor

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
)

func TestReportProtocolGate_currentDaemonSaysNothing(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, reportProtocolGate(&buf, daemon.DaemonInfo{Protocol: daemon.MinDaemonProtocol}))
	require.NoError(t, reportProtocolGate(&buf, daemon.DaemonInfo{}))
	assert.Empty(t, buf.String())
}

// Doctor exists to name what is broken; returning the bare refusal made it
// refuse to diagnose the one condition it is most needed for.
func TestReportProtocolGate_tooOldDaemonIsReportedAsACheck(t *testing.T) {
	if daemon.MinDaemonProtocol <= 1 {
		t.Skip("no rejectable protocol below MinDaemonProtocol")
	}
	var buf bytes.Buffer
	err := reportProtocolGate(&buf, daemon.DaemonInfo{Protocol: daemon.MinDaemonProtocol - 1})
	require.Error(t, err)
	assert.True(t, daemon.IsProtocolError(err))
	out := buf.String()
	assert.Contains(t, out, "✗ daemon protocol")
	assert.Contains(t, out, "human daemon restart")
}

func TestPrintCheck_rendersSeverity(t *testing.T) {
	var buf bytes.Buffer
	printCheck(&buf, daemon.DoctorCheck{Name: "docker", OK: true}, "running")
	printCheck(&buf, daemon.DoctorCheck{Name: "tracker", OK: false, Severity: daemon.SeverityBlocking}, "no credentials")
	printCheck(&buf, daemon.DoctorCheck{Name: "proxy", OK: false, Severity: daemon.SeverityTransient}, "failed once")
	out := buf.String()
	assert.Contains(t, out, "✓ docker — running")
	assert.Contains(t, out, "✗ tracker — no credentials")
	assert.Contains(t, out, "! proxy — failed once")
}
