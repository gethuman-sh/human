package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
)

// --- fakes ---

type fakeSnapshots struct {
	dto SnapshotDTO
	err error
}

func (f fakeSnapshots) Snapshot(context.Context) (SnapshotDTO, error) { return f.dto, f.err }

type fakeLogMode struct {
	mode string
	err  error
}

func (f *fakeLogMode) Get() string { return f.mode }
func (f *fakeLogMode) Set(mode string) error {
	if f.err != nil {
		return f.err
	}
	f.mode = mode
	return nil
}

type fakeCommands struct {
	gotArgs []string
	out     []byte
	err     error
}

func (f *fakeCommands) RunCapture(args []string) ([]byte, error) {
	f.gotArgs = args
	return f.out, f.err
}

type fakeAgents struct {
	dispatched DispatchOpts
	stopped    string
	name       string
	err        error
}

func (f *fakeAgents) Dispatch(_ context.Context, opts DispatchOpts) (string, error) {
	f.dispatched = opts
	return f.name, f.err
}

func (f *fakeAgents) Stop(_ context.Context, name string) error {
	f.stopped = name
	return f.err
}

// --- endpoint tests ---

func TestHandleSnapshot(t *testing.T) {
	s := testServer()
	s.Snapshots = fakeSnapshots{dto: SnapshotDTO{Hostname: "h1"}}

	rec := doReq(t, s, authedReq(http.MethodGet, "/api/snapshot", ""))
	require.Equal(t, http.StatusOK, rec.Code)

	var dto SnapshotDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &dto))
	assert.Equal(t, "h1", dto.Hostname)
}

func TestHandleSnapshot_Unavailable(t *testing.T) {
	s := testServer()
	rec := doReq(t, s, authedReq(http.MethodGet, "/api/snapshot", ""))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHandleSnapshot_Error(t *testing.T) {
	s := testServer()
	s.Snapshots = fakeSnapshots{err: errors.WithDetails("boom")}
	rec := doReq(t, s, authedReq(http.MethodGet, "/api/snapshot", ""))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHandleProjects(t *testing.T) {
	s := testServer()
	s.Projects = func() []daemon.ProjectInfo {
		return []daemon.ProjectInfo{{Name: "cli", Dir: "/home/u/cli"}}
	}

	rec := doReq(t, s, authedReq(http.MethodGet, "/api/projects", ""))
	require.Equal(t, http.StatusOK, rec.Code)

	var projects []daemon.ProjectInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &projects))
	require.Len(t, projects, 1)
	assert.Equal(t, "cli", projects[0].Name)
}

func TestHandleIssues(t *testing.T) {
	s := testServer()
	s.Issues = func() ([]daemon.TrackerIssuesResult, error) {
		return []daemon.TrackerIssuesResult{{TrackerKind: "linear", Project: "HUM"}}, nil
	}

	rec := doReq(t, s, authedReq(http.MethodGet, "/api/issues", ""))
	require.Equal(t, http.StatusOK, rec.Code)

	var results []daemon.TrackerIssuesResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &results))
	require.Len(t, results, 1)
	assert.Equal(t, "linear", results[0].TrackerKind)
}

func TestHandleIssues_FetchError(t *testing.T) {
	s := testServer()
	s.Issues = func() ([]daemon.TrackerIssuesResult, error) {
		return nil, errors.WithDetails("tracker down")
	}
	rec := doReq(t, s, authedReq(http.MethodGet, "/api/issues", ""))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestHandleIssues_NilFetcherReturnsEmpty(t *testing.T) {
	s := testServer()
	rec := doReq(t, s, authedReq(http.MethodGet, "/api/issues", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, "[]", rec.Body.String())
}

func TestHandleLogMode_GetAndSet(t *testing.T) {
	s := testServer()
	lm := &fakeLogMode{mode: "meta"}
	s.LogMode = lm

	rec := doReq(t, s, authedReq(http.MethodGet, "/api/log-mode", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"mode":"meta"}`, rec.Body.String())

	rec = doReq(t, s, authedReq(http.MethodPut, "/api/log-mode", `{"mode":"full"}`))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"mode":"full"}`, rec.Body.String())
	assert.Equal(t, "full", lm.mode)
}

func TestHandleLogMode_SetInvalid(t *testing.T) {
	s := testServer()
	s.LogMode = &fakeLogMode{err: errors.WithDetails("bad mode")}

	rec := doReq(t, s, authedReq(http.MethodPut, "/api/log-mode", `{"mode":"nope"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = doReq(t, s, authedReq(http.MethodPut, "/api/log-mode", `not json`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleConfirms_GetAndResolve(t *testing.T) {
	s := testServer()
	store := daemon.NewPendingConfirmStore()
	s.Confirms = store
	// The daemon resolves confirmations under its own PID; the requesting
	// Claude instance PID differs, so the store's self-approval guard must
	// not fire for GUI approvals.
	s.ApproverPID = os.Getpid()

	decision := make(chan bool, 1)
	store.Add(&daemon.PendingConfirmation{
		ID: "op-1", Operation: "DeleteIssue", Tracker: "linear", Key: "HUM-1",
		Prompt: "Delete HUM-1?", ClientPID: 999999, CreatedAt: time.Now(),
		Decision: decision,
	})

	rec := doReq(t, s, authedReq(http.MethodGet, "/api/confirms", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	var pending []daemon.PendingConfirm
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &pending))
	require.Len(t, pending, 1)
	assert.Equal(t, "op-1", pending[0].ID)

	rec = doReq(t, s, authedReq(http.MethodPost, "/api/confirms/op-1", `{"approved":true}`))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, <-decision)
	assert.Equal(t, 0, store.Len())
}

func TestHandleConfirms_UnknownID(t *testing.T) {
	s := testServer()
	s.Confirms = daemon.NewPendingConfirmStore()
	s.ApproverPID = os.Getpid()

	rec := doReq(t, s, authedReq(http.MethodPost, "/api/confirms/nope", `{"approved":false}`))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleTicketCreate(t *testing.T) {
	s := testServer()
	cmds := &fakeCommands{out: []byte("HUM-7\tother\n")}
	s.Commands = cmds

	body := `{"tracker_kind":"linear","project":"HUM","title":"New thing","description":"Details"}`
	rec := doReq(t, s, authedReq(http.MethodPost, "/api/tickets", body))
	require.Equal(t, http.StatusCreated, rec.Code)

	var created ticketCreated
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.Equal(t, "HUM-7", created.Key)
	assert.Equal(t, "linear", created.TrackerKind)

	// Same arg shape as the TUI dispatches over the daemon loopback.
	assert.Equal(t, []string{"linear", "issue", "create", "--project=HUM", "New thing", "--description", "Details"}, cmds.gotArgs)
}

func TestHandleTicketCreate_Validation(t *testing.T) {
	s := testServer()
	s.Commands = &fakeCommands{}

	rec := doReq(t, s, authedReq(http.MethodPost, "/api/tickets", `{"title":"no kind"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = doReq(t, s, authedReq(http.MethodPost, "/api/tickets", `{"tracker_kind":"linear"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = doReq(t, s, authedReq(http.MethodPost, "/api/tickets", `not json`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleTicketCreate_RunError(t *testing.T) {
	s := testServer()
	s.Commands = &fakeCommands{err: errors.WithDetails("daemon unreachable")}

	body := `{"tracker_kind":"linear","project":"HUM","title":"x"}`
	rec := doReq(t, s, authedReq(http.MethodPost, "/api/tickets", body))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestHandleAgentDispatch(t *testing.T) {
	s := testServer()
	agents := &fakeAgents{name: "agent-3"}
	s.Agents = agents

	body := `{"prompt":"/human-execute HUM-9","project_dir":"/home/u/proj"}`
	rec := doReq(t, s, authedReq(http.MethodPost, "/api/agents", body))
	require.Equal(t, http.StatusAccepted, rec.Code)

	var resp agentDispatched
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "agent-3", resp.Name)
	assert.Equal(t, "/human-execute HUM-9", agents.dispatched.Prompt)
	assert.Equal(t, "/home/u/proj", agents.dispatched.ProjectDir)
}

func TestHandleAgentDispatch_Validation(t *testing.T) {
	s := testServer()
	s.Agents = &fakeAgents{}

	rec := doReq(t, s, authedReq(http.MethodPost, "/api/agents", `{"prompt":""}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	s.Agents = nil
	rec = doReq(t, s, authedReq(http.MethodPost, "/api/agents", `{"prompt":"x"}`))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHandleAgentStop(t *testing.T) {
	s := testServer()
	agents := &fakeAgents{}
	s.Agents = agents

	rec := doReq(t, s, authedReq(http.MethodPost, "/api/agents/agent-5/stop", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "agent-5", agents.stopped)
}

func TestHandleAgentStop_Error(t *testing.T) {
	s := testServer()
	s.Agents = &fakeAgents{err: errors.WithDetails("no such agent")}

	rec := doReq(t, s, authedReq(http.MethodPost, "/api/agents/agent-5/stop", ""))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
