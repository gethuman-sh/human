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

func whereServer(reader WhereCommentReader) *Server {
	return &Server{Logger: zerolog.Nop(), WhereComments: reader}
}

func whereRequest(t *testing.T, srv *Server, req WhereRequest) Response {
	t.Helper()
	arg, err := json.Marshal(req)
	require.NoError(t, err)
	return captureHandlerResponse(t, func(conn net.Conn) {
		srv.handleFSMWhere(conn, []string{string(arg)})
	})
}

func TestFSMWhereRoute_AnswersForATicket(t *testing.T) {
	srv := whereServer(func(string) ([]tracker.Comment, tracker.Category, bool, error) {
		return []tracker.Comment{cmt(PlanReadyHeader, time.Unix(1000, 0))}, tracker.CategoryUnstarted, false, nil
	})

	resp := whereRequest(t, srv, WhereRequest{Key: "SC-1"})

	require.Equal(t, 0, resp.ExitCode, resp.Stderr)
	var report WhereReport
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &report))
	assert.Equal(t, "SC-1", report.Key)
	assert.Equal(t, "planned", report.State)
	assert.Positive(t, report.DocumentVersion)
}

// The default asker is the one that owns the fewest edges, so an unstated actor
// withholds a command rather than offering one the caller may not use.
func TestFSMWhereRoute_DefaultsToTheSkillActor(t *testing.T) {
	srv := whereServer(func(string) ([]tracker.Comment, tracker.Category, bool, error) {
		return []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1000, 0))}, tracker.CategoryUnstarted, false, nil
	})

	stated := whereRequest(t, srv, WhereRequest{Key: "SC-1", Actor: "skill"})
	unstated := whereRequest(t, srv, WhereRequest{Key: "SC-1"})

	assert.Equal(t, stated.Stdout, unstated.Stdout)
}

func TestFSMWhereRoute_RejectsAnActorTheMachineDoesNotDeclare(t *testing.T) {
	srv := whereServer(func(string) ([]tracker.Comment, tracker.Category, bool, error) {
		return nil, tracker.CategoryUnstarted, false, nil
	})

	resp := whereRequest(t, srv, WhereRequest{Key: "SC-1", Actor: "robot"})

	assert.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "no such actor")
}

func TestFSMWhereRoute_NeedsAKey(t *testing.T) {
	srv := whereServer(func(string) ([]tracker.Comment, tracker.Category, bool, error) {
		return nil, tracker.CategoryUnstarted, false, nil
	})

	resp := whereRequest(t, srv, WhereRequest{})

	assert.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "needs a ticket key")
}

// An unreadable thread must fail rather than answer "backlog, nothing here" —
// a confident wrong answer where none is far cheaper.
func TestFSMWhereRoute_FailsWhenTheThreadCannotBeRead(t *testing.T) {
	srv := whereServer(func(string) ([]tracker.Comment, tracker.Category, bool, error) {
		return nil, "", false, assert.AnError
	})

	resp := whereRequest(t, srv, WhereRequest{Key: "SC-1"})

	assert.NotEqual(t, 0, resp.ExitCode)
}

func TestFSMWhereRoute_DisabledWithoutAReader(t *testing.T) {
	resp := whereRequest(t, &Server{Logger: zerolog.Nop()}, WhereRequest{Key: "SC-1"})

	assert.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "cannot read")
}
