package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOAuthRelayCountsAsBlockingOp proves a browser sign-in parks the daemon's
// callback listener as restart-blocking work. Without this, a binary-change
// self-restart could tear the listener down mid-login and strand the callback,
// failing the user's sign-in.
func TestOAuthRelayCountsAsBlockingOp(t *testing.T) {
	// A free port to serve as the OAuth redirect_uri target; the daemon binds
	// it while waiting for the provider to call back.
	redirectLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	redirectPort := redirectLn.Addr().(*net.TCPAddr).Port
	_ = redirectLn.Close()

	oauthURL := fmt.Sprintf(
		"https://auth.example.com/authorize?redirect_uri=%s&client_id=test",
		url.QueryEscape(fmt.Sprintf("http://localhost:%d/callback", redirectPort)),
	)

	const token = "oauth-blocking-token"
	opener := &mockOpener{opened: make(chan string, 1)}
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		CmdFactory: echoCmd,
		Opener:     opener,
		Logger:     zerolog.Nop(),
	}

	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	srv.Addr = ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.ListenAndServe(ctx) }()

	require.Eventually(t, func() bool {
		c, dialErr := net.DialTimeout("tcp", srv.Addr, 100*time.Millisecond)
		if dialErr != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 2*time.Second, 20*time.Millisecond)

	assert.Equal(t, 0, srv.BlockingOps(), "idle daemon has no blocking work")

	// Kick off the browser OAuth flow and leave it parked awaiting the callback.
	conn, err := net.DialTimeout("tcp", srv.Addr, 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	require.NoError(t, json.NewEncoder(conn).Encode(
		Request{Version: "dev", Token: token, Args: []string{"browser", oauthURL}}))

	// Once the browser has been opened the relay is waiting on the callback.
	select {
	case <-opener.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("browser was never opened; OAuth relay did not start")
	}

	require.Eventually(t, func() bool {
		return srv.BlockingOps() > 0
	}, 2*time.Second, 20*time.Millisecond,
		"an in-flight OAuth login must block a self-restart handover")
}
