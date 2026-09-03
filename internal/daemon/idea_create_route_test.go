package daemon

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func newTestIdeaCreator(creator tracker.Creator, project string, notify func()) *IdeaCreator {
	return &IdeaCreator{
		ResolveCreator: func() (tracker.Creator, string, error) { return creator, project, nil },
		Notify:         notify,
	}
}

func TestServer_ideaCreateRoute(t *testing.T) {
	creator := newFakeCreator()
	srv := &Server{Logger: zerolog.Nop(), IdeaCreator: newTestIdeaCreator(creator, "proj", nil)}

	resp := captureHandlerResponse(t, func(conn net.Conn) {
		srv.handleIdeaCreate(conn, []string{`{"title":"Weekly digest email"}`})
	})
	require.Equal(t, 0, resp.ExitCode, "stderr: %s", resp.Stderr)
	var out IdeaCreateResponse
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &out))
	assert.Equal(t, "SC-999", out.Key)

	captured := creator.capturedIssue()
	require.NotNil(t, captured)
	assert.Equal(t, []string{tracker.IdeaLabel}, captured.Labels)
}

// Capture IS the ask for a draft, so the create route fires the drafter — and
// it does so without making the user wait for a container to start.
func TestHandleIdeaCreate_FiresTheDrafter(t *testing.T) {
	launched := make(chan IdeaDraftRequest, 1)
	srv := &Server{
		Logger:      zerolog.Nop(),
		IdeaCreator: newTestIdeaCreator(newFakeCreator(), "proj", nil),
		IdeaDraftLauncher: func(req IdeaDraftRequest) error {
			launched <- req
			return nil
		},
	}

	resp := captureHandlerResponse(t, func(conn net.Conn) {
		srv.handleIdeaCreate(conn, []string{`{"title":"Weekly digest email"}`})
	})
	require.Equal(t, 0, resp.ExitCode, "stderr: %s", resp.Stderr)

	select {
	case req := <-launched:
		assert.Equal(t, "SC-999", req.Key)
		assert.Equal(t, "Weekly digest email", req.Title)
	case <-time.After(2 * time.Second):
		t.Fatal("capturing an idea must fire the background drafter")
	}
}

// No substrate wiring degrades to no draft, never to an error at capture.
func TestHandleIdeaCreate_SurvivesANilLauncher(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop(), IdeaCreator: newTestIdeaCreator(newFakeCreator(), "proj", nil)}

	resp := captureHandlerResponse(t, func(conn net.Conn) {
		srv.handleIdeaCreate(conn, []string{`{"title":"Weekly digest email"}`})
	})
	assert.Equal(t, 0, resp.ExitCode, "stderr: %s", resp.Stderr)
}

func TestServer_ideaCreateRouteBadInput(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop(), IdeaCreator: newTestIdeaCreator(newFakeCreator(), "proj", nil)}
	for name, args := range map[string][]string{
		"no arg":       {},
		"invalid json": {"{broken"},
		"empty title":  {`{"title":"  "}`},
	} {
		resp := captureHandlerResponse(t, func(conn net.Conn) { srv.handleIdeaCreate(conn, args) })
		assert.Equal(t, 1, resp.ExitCode, "case %s", name)
	}
}

// The route is off, not broken, when nothing wired a creator.
func TestServer_ideaCreateRouteNilCreator(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}
	resp := captureHandlerResponse(t, func(conn net.Conn) {
		srv.handleIdeaCreate(conn, []string{`{"title":"x"}`})
	})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "idea creation not available")
}
