package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/proxy"
)

func TestServer_modelOutcomesRoute_empty(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}

	resp := captureHandlerResponse(t, srv.handleModelOutcomes)

	assert.Equal(t, "[]\n", resp.Stdout)
	assert.Empty(t, resp.Stderr)
	assert.Equal(t, 0, resp.ExitCode)
}

func TestServer_modelOutcomesRoute_populated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := NewModelOutcomeSink(ctx)
	sink.Record(proxy.ModelCallOutcome{Ticket: "SC-2555", Stage: "implementation", Host: "api.anthropic.com", Class: proxy.ClassOK, StatusCode: 200})

	require.Eventually(t, func() bool { return len(sink.Outcomes()) == 1 }, time.Second, 10*time.Millisecond)

	srv := &Server{Logger: zerolog.Nop(), ModelOutcomes: sink}
	resp := captureHandlerResponse(t, srv.handleModelOutcomes)
	require.NotEmpty(t, resp.Stdout)

	var outcomes []proxy.ModelCallOutcome
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &outcomes))
	require.Len(t, outcomes, 1)
	assert.Equal(t, "SC-2555", outcomes[0].Ticket)
	assert.Equal(t, proxy.ClassOK, outcomes[0].Class)
}
