package daemon

import (
	"net"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

// The route's whole job: reach the SAME drafter the Ideas lane uses, carrying
// the one flag that says a person asked for this rewrite.
func TestHandleRecreateDescription_LaunchesTheDrafterWithTheRecreateFlag(t *testing.T) {
	var got IdeaDraftRequest
	srv := &Server{
		Logger: zerolog.Nop(),
		IdeaDraftLauncher: func(req IdeaDraftRequest) error {
			got = req
			return nil
		},
	}

	resp := captureHandlerResponse(t, func(conn net.Conn) {
		srv.handleRecreateDescription(conn, []string{`{"key":"SC-1","title":"a promoted ticket"}`})
	})
	require.Equal(t, 0, resp.ExitCode, "stderr: %s", resp.Stderr)
	assert.Equal(t, "SC-1", got.Key)
	assert.Equal(t, "a promoted ticket", got.Title)
	assert.True(t, got.Recreate, "the click is what bypasses the overwrite guard")
}

// Unlike idea-create's fire-and-forget launch, a recreate that never starts
// must reach the board's error banner rather than looking like a rewrite that
// produced nothing.
func TestHandleRecreateDescription_ReturnsTheLaunchError(t *testing.T) {
	srv := &Server{
		Logger: zerolog.Nop(),
		IdeaDraftLauncher: func(IdeaDraftRequest) error {
			return errors.WithDetails("no docker", "key", "SC-1")
		},
	}

	resp := captureHandlerResponse(t, func(conn net.Conn) {
		srv.handleRecreateDescription(conn, []string{`{"key":"SC-1"}`})
	})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "no docker")
}

func TestHandleRecreateDescription_BadInput(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop(), IdeaDraftLauncher: func(IdeaDraftRequest) error { return nil }}
	for name, args := range map[string][]string{
		"no arg":       {},
		"two args":     {`{"key":"SC-1"}`, "extra"},
		"invalid json": {"{broken"},
		"empty key":    {`{"key":"  "}`},
	} {
		resp := captureHandlerResponse(t, func(conn net.Conn) { srv.handleRecreateDescription(conn, args) })
		assert.Equal(t, 1, resp.ExitCode, "case %s", name)
	}
}

// The route is off, not broken, when nothing wired a launcher.
func TestHandleRecreateDescription_NilLauncher(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}
	resp := captureHandlerResponse(t, func(conn net.Conn) {
		srv.handleRecreateDescription(conn, []string{`{"key":"SC-1"}`})
	})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "description recreation not available")
}
