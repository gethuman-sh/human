package monarch

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_handleConn_insertsLines(t *testing.T) {
	store := newTestStore(t)
	srv := &Server{Store: store, Logger: zerolog.Nop()}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		srv.handleConn(context.Background(), server)
		close(done)
	}()

	// Two valid events bracket one malformed line that must be skipped.
	payload := `{"type":"agent.start","daemon_id":"d1","agent_id":"a1","state":"coding","ts":"2026-06-26T10:00:00Z"}` + "\n" +
		`{not json}` + "\n" +
		`{"type":"agent.start","daemon_id":"d2","agent_id":"a2","state":"coding","ts":"2026-06-26T10:00:00Z"}` + "\n"
	_, err := io.WriteString(client, payload)
	require.NoError(t, err)
	require.NoError(t, client.Close())

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConn did not return after EOF")
	}

	board, err := store.WorkBoard(context.Background(), time.Time{})
	require.NoError(t, err)
	assert.Len(t, board, 2, "two valid events stored, malformed line skipped")
}

func TestServer_ListenAndServe_stopsOnCtxCancel(t *testing.T) {
	store := newTestStore(t)
	srv := &Server{Addr: "127.0.0.1:0", Store: store, Logger: zerolog.Nop()}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()

	// Give the listener a moment to bind, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return after ctx cancel")
	}
}
