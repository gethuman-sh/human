package cmdproxy

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/proxy"
)

func TestWriteModelOutcomes_Empty(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, writeModelOutcomes(&buf, nil, false))
	assert.Contains(t, buf.String(), "No model-call outcomes recorded.")
}

func TestWriteModelOutcomes_Table(t *testing.T) {
	var buf bytes.Buffer
	outcomes := []proxy.ModelCallOutcome{
		{Ticket: "SC-2555", Stage: "implementation", Host: "api.anthropic.com", StatusCode: 200, Class: "ok", StartedAt: time.Unix(1_700_000_000, 0).UTC(), Duration: 1200 * time.Millisecond},
		{Host: "api.anthropic.com", StatusCode: 0, Class: "network", StartedAt: time.Unix(1_700_000_001, 0).UTC()},
	}
	require.NoError(t, writeModelOutcomes(&buf, outcomes, false))
	out := buf.String()
	assert.Contains(t, out, "ok")
	assert.Contains(t, out, "SC-2555/implementation")
	assert.Contains(t, out, "status=200")
	assert.Contains(t, out, "network")
	assert.Contains(t, out, "(unattributed)/-", "an unattributed outcome renders with placeholders")
}

func TestWriteModelOutcomes_JSON(t *testing.T) {
	var buf bytes.Buffer
	outcomes := []proxy.ModelCallOutcome{
		{Ticket: "SC-2555", Class: "ok", StatusCode: 200},
	}
	require.NoError(t, writeModelOutcomes(&buf, outcomes, true))

	var got []proxy.ModelCallOutcome
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "SC-2555", got[0].Ticket)
}
