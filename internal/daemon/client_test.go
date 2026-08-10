package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startMockDaemon(t *testing.T, handler func(req Request) Response) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()

				scanner := bufio.NewScanner(conn)
				if !scanner.Scan() {
					return
				}
				var req Request
				if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
					return
				}

				resp := handler(req)
				enc := json.NewEncoder(conn)
				_ = enc.Encode(resp)
			}()
		}
	}()

	return ln.Addr().String()
}

// newTestClient points a Client at a mock daemon's address. It bypasses
// NewClient because a mock advertises no protocol and the gate is exercised on
// its own in version_gate_test.go.
func newTestClient(addr, token string) *Client {
	return &Client{info: DaemonInfo{Addr: addr, Token: token}, version: "dev"}
}

func TestRunRemote_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, "test-token", req.Token)
		assert.Equal(t, []string{"echo", "hello"}, req.Args)
		return Response{
			Stdout:   "hello\n",
			ExitCode: 0,
		}
	})

	exitCode, err := newTestClient(addr, "test-token").RunRemote([]string{"echo", "hello"})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestRunRemote_NonZeroExit(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{
			Stderr:   "error occurred\n",
			ExitCode: 1,
		}
	})

	exitCode, err := newTestClient(addr, "tok").RunRemote([]string{"fail"})
	require.NoError(t, err)
	assert.Equal(t, 1, exitCode)
}

func TestGetNetworkEvents_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"network-events"}, req.Args)
		// Two-event payload mirrors the handleNetworkEvents wire format.
		data := `[{"source":"proxy","status":"forward","host":"github.com","count":3,"last_seen":"2024-01-01T00:00:00Z"},` +
			`{"source":"fail","status":"dial-fail","host":"broken.example.com","count":1,"last_seen":"2024-01-01T00:00:05Z"}]` + "\n"
		return Response{Stdout: data}
	})

	events, err := newTestClient(addr, "tok").GetNetworkEvents()
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "github.com", events[0].Host)
	assert.Equal(t, 3, events[0].Count)
	assert.Equal(t, "broken.example.com", events[1].Host)
	assert.Equal(t, "dial-fail", events[1].Status)
}

func TestGetNetworkEvents_Empty(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "[]\n"}
	})

	events, err := newTestClient(addr, "tok").GetNetworkEvents()
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestGetNetworkEvents_InvalidJSON(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "not json\n"}
	})

	_, err := newTestClient(addr, "tok").GetNetworkEvents()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid network events JSON")
}

func TestGetModelOutcomes_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"model-outcomes"}, req.Args)
		data := `[{"ticket":"SC-2555","stage":"implementation","host":"api.anthropic.com","status_code":200,"class":"ok","started_at":"2024-01-01T00:00:00Z","duration":1000000}]` + "\n"
		return Response{Stdout: data}
	})

	outcomes, err := newTestClient(addr, "tok").GetModelOutcomes()
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	assert.Equal(t, "SC-2555", outcomes[0].Ticket)
	assert.Equal(t, "ok", outcomes[0].Class)
	assert.Equal(t, 200, outcomes[0].StatusCode)
}

func TestGetModelOutcomes_Empty(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "[]\n"}
	})

	outcomes, err := newTestClient(addr, "tok").GetModelOutcomes()
	require.NoError(t, err)
	assert.Empty(t, outcomes)
}

func TestGetModelOutcomes_InvalidJSON(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "not json\n"}
	})

	_, err := newTestClient(addr, "tok").GetModelOutcomes()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid model outcomes JSON")
}

func TestGetTicketCost_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"ticket-cost", "SC-1"}, req.Args)
		data := `{"ticket":"SC-1","hasSpend":true,"totalCostUSD":0.5,"contextCostUSD":0.3,"answersCostUSD":0.2,"totalDurationMs":3000,` +
			`"stages":[{"stage":"planning","costUSD":0.5,"contextCostUSD":0.3,"answersCostUSD":0.2,"durationMs":3000}]}` + "\n"
		return Response{Stdout: data}
	})

	rollup, err := newTestClient(addr, "tok").GetTicketCost("SC-1")
	require.NoError(t, err)
	assert.True(t, rollup.HasSpend)
	assert.Equal(t, "SC-1", rollup.Ticket)
	assert.InDelta(t, 0.5, rollup.TotalCostUSD, 1e-9)
	assert.Equal(t, int64(3000), rollup.TotalDurationMs)
	require.Len(t, rollup.Stages, 1)
	assert.Equal(t, "planning", rollup.Stages[0].Stage)
}

func TestGetTicketCost_InvalidJSON(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "not json\n"}
	})

	_, err := newTestClient(addr, "tok").GetTicketCost("SC-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding ticket cost")
}

func TestRunRemote_ConnectionRefused(t *testing.T) {
	exitCode, err := newTestClient("127.0.0.1:1", "tok").RunRemote([]string{"echo"})
	require.Error(t, err)
	assert.Equal(t, 1, exitCode)
	assert.Contains(t, err.Error(), "cannot reach daemon")
}

func TestGetIdeationStatus_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"ideation-status"}, req.Args)
		data := `{"session_id":"ideation-1","state":"awaiting_reply"}` + "\n"
		return Response{Stdout: data}
	})

	st, err := newTestClient(addr, "tok").GetIdeationStatus()
	require.NoError(t, err)
	assert.Equal(t, "ideation-1", st.SessionID)
	assert.Equal(t, IdeationAwaitingReply, st.State)
}

func TestIdeationStart_ErrorPropagates(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stderr: "ideation not available\n", ExitCode: 1}
	})

	_, err := newTestClient(addr, "tok").IdeationStart(IdeationStartRequest{Seed: "idea"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon command failed")
}

func TestIdeationReply_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		require.Len(t, req.Args, 2)
		assert.Equal(t, "ideation-reply", req.Args[0])
		data := `{"session_id":"ideation-1","state":"thinking"}` + "\n"
		return Response{Stdout: data}
	})

	st, err := newTestClient(addr, "tok").IdeationReply(IdeationReplyRequest{SessionID: "ideation-1", Message: "answer"})
	require.NoError(t, err)
	assert.Equal(t, IdeationThinking, st.State)
}

func TestGetIdeationStatus_InvalidJSON(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "not json\n"}
	})

	_, err := newTestClient(addr, "tok").GetIdeationStatus()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ideation status JSON")
}

func TestRunRemote_VersionForwarded(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, "1.2.3", req.Version)
		return Response{ExitCode: 0}
	})

	c := newTestClient(addr, "tok")
	c.version = "1.2.3"
	exitCode, err := c.RunRemote([]string{})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestRunRemote_EnvForwarded(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, "1", req.Env["NO_COLOR"])
		return Response{ExitCode: 0}
	})

	exitCode, err := newTestClient(addr, "tok").RunRemote([]string{})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestRunRemote_ClientPIDForwarded(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Greater(t, req.ClientPID, 0, "ClientPID should be set to parent PID")
		return Response{ExitCode: 0}
	})

	exitCode, err := newTestClient(addr, "tok").RunRemote([]string{})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestGetLogMode_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"log-mode"}, req.Args)
		return Response{Stdout: "full\n"}
	})

	mode, err := newTestClient(addr, "tok").GetLogMode()
	require.NoError(t, err)
	assert.Equal(t, "full", mode)
}

func TestSetLogMode_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"log-mode", "off"}, req.Args)
		return Response{Stdout: "off\n"}
	})

	mode, err := newTestClient(addr, "tok").SetLogMode("off")
	require.NoError(t, err)
	assert.Equal(t, "off", mode)
}

func TestGetHookSnapshot_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"hook-snapshot"}, req.Args)
		return Response{Stdout: `{"session-1":{"session_id":"session-1","cwd":"/proj","status":1}}` + "\n"}
	})

	snap, err := newTestClient(addr, "tok").GetHookSnapshot()
	require.NoError(t, err)
	require.Contains(t, snap, "session-1")
	assert.Equal(t, "/proj", snap["session-1"].Cwd)
}

func TestGetHookSnapshot_InvalidJSON(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "bad json\n"}
	})

	_, err := newTestClient(addr, "tok").GetHookSnapshot()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid hook snapshot JSON")
}

func TestGetTrackerDiagnose_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"tracker-diagnose"}, req.Args)
		return Response{Stdout: `[{"name":"jira","ok":true}]` + "\n"}
	})

	statuses, err := newTestClient(addr, "tok").GetTrackerDiagnose()
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, "jira", statuses[0].Name)
}

func TestGetTrackerIssues_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"tracker-issues"}, req.Args)
		return Response{Stdout: `[{"tracker_name":"jira","tracker_kind":"jira","project":"PROJ","issues":[]}]` + "\n"}
	})

	results, err := newTestClient(addr, "tok").GetTrackerIssues()
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "jira", results[0].TrackerName)
}

func TestGetTrackerIssuesLite_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		// The lite path must hit its own command so the daemon skips the
		// per-ticket comment scan.
		assert.Equal(t, []string{"tracker-issues-lite"}, req.Args)
		return Response{Stdout: `[{"tracker_name":"jira","tracker_kind":"jira","project":"PROJ","issues":[]}]` + "\n"}
	})

	results, err := newTestClient(addr, "tok").GetTrackerIssuesLite()
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "jira", results[0].TrackerName)
}

func TestGetPendingConfirms_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"pending-confirms"}, req.Args)
		return Response{Stdout: `[{"id":"abc","prompt":"delete?"}]` + "\n"}
	})

	confirms, err := newTestClient(addr, "tok").GetPendingConfirms()
	require.NoError(t, err)
	require.Len(t, confirms, 1)
	assert.Equal(t, "abc", confirms[0].ID)
}

func TestGetToolStats_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"tool-stats"}, req.Args)
		return Response{Stdout: `{"total_events":5,"by_tool":[],"by_event_name":[],"by_hour":[]}` + "\n"}
	})

	ts, err := newTestClient(addr, "tok").GetToolStats()
	require.NoError(t, err)
	assert.Equal(t, 5, ts.TotalEvents)
}

func TestSendConfirmDecision_Approved(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"confirm-op", "abc", "yes"}, req.Args)
		return Response{ExitCode: 0}
	})

	err := newTestClient(addr, "tok").SendConfirmDecision("abc", true)
	require.NoError(t, err)
}

func TestSendConfirmDecision_Denied(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"confirm-op", "abc", "no"}, req.Args)
		return Response{ExitCode: 0}
	})

	err := newTestClient(addr, "tok").SendConfirmDecision("abc", false)
	require.NoError(t, err)
}

func TestStartFindbugs_SendsRoute(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"findbugs-start"}, req.Args)
		return Response{ExitCode: 0, Stdout: "ok\n"}
	})

	err := newTestClient(addr, "tok").StartFindbugs()
	require.NoError(t, err)
}

func TestStartFindbugs_ErrorPropagates(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{ExitCode: 1, Stderr: "findbugs sweep not available"}
	})

	err := newTestClient(addr, "tok").StartFindbugs()
	require.Error(t, err)
}

func TestRunRemoteCapture_DaemonError(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{ExitCode: 1, Stderr: "some error"}
	})

	_, err := newTestClient(addr, "tok").RunRemoteCapture([]string{"bad-cmd"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon command failed")
}

func TestSelectedEnv(t *testing.T) {
	// With env vars set.
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm")
	env := selectedEnv()
	assert.Equal(t, "1", env["NO_COLOR"])
	assert.Equal(t, "xterm", env["TERM"])
}

func TestHandleOAuthCallback(t *testing.T) {
	// Simulate the two-line OAuth protocol.
	callbackResp := Response{Callback: ""}
	data, _ := json.Marshal(callbackResp)

	reader := bufio.NewReader(strings.NewReader(string(data) + "\n"))
	code, err := handleOAuthCallback(reader)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
}

func TestHandleOAuthCallback_ReadError(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	_, err := handleOAuthCallback(reader)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read callback response")
}

// A client newer than the daemon fails with cobra's unknown-command error,
// which reads like a typo. The hint names the real cause, since the version
// gate only refuses a too-old client and never a too-old daemon.
func TestStaleDaemonHint(t *testing.T) {
	hint := staleDaemonHint(`Error: unknown command "state" for "human"`)
	if !strings.Contains(hint, "daemon") || !strings.Contains(hint, "daemon start") {
		t.Errorf("hint should explain the stale daemon and how to fix it, got %q", hint)
	}

	if got := staleDaemonHint("Error: ticket not found"); got != "" {
		t.Errorf("unrelated stderr must get no hint, got %q", got)
	}
	if got := staleDaemonHint(""); got != "" {
		t.Errorf("empty stderr must get no hint, got %q", got)
	}
}

func TestNewConfirmID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id := newConfirmID()
		assert.True(t, strings.HasPrefix(id, "c-"))
		assert.False(t, seen[id], "confirm IDs must be unique")
		seen[id] = true
	}
}

// TestRunRemote_AwaitConfirmReturnsImmediately pins the no-waiting contract:
// an await_confirm answer produces exactly one request (no confirm-status
// polling, no re-submit) and exit code 1 with instructions — the user
// approves on their own time and re-runs.
func TestRunRemote_AwaitConfirmReturnsImmediately(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	addr := startMockDaemon(t, func(req Request) Response {
		mu.Lock()
		defer mu.Unlock()
		requests++
		require.NotEqual(t, "confirm-status", req.Args[0], "no-wait client must not poll")
		return Response{AwaitConfirm: true, ConfirmID: req.ConfirmID, ConfirmPrompt: "Delete KAN-1?"}
	})

	start := time.Now()
	code, err := newTestClient(addr, "tok").RunRemote([]string{"jira", "issue", "delete", "KAN-1"})
	require.NoError(t, err)
	assert.Equal(t, 1, code)
	assert.Less(t, time.Since(start), time.Second, "there is no countdown")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, requests, "queue once, return immediately")
}

func TestGetConfirmStatus_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		require.GreaterOrEqual(t, len(req.Args), 2)
		assert.Equal(t, "confirm-status", req.Args[0])
		data, _ := json.Marshal(ConfirmStatus{ID: req.Args[1], State: string(ConfirmApproved)})
		return Response{Stdout: string(data) + "\n"}
	})
	st, err := newTestClient(addr, "tok").GetConfirmStatus("c-9")
	require.NoError(t, err)
	assert.Equal(t, string(ConfirmApproved), st.State)
}

func TestRunRemote_DaemonClosesImmediately(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	exitCode, err := newTestClient(ln.Addr().String(), "tok").RunRemote([]string{})
	require.Error(t, err)
	assert.Equal(t, 1, exitCode)
	// Depending on timing, the error may be a clean EOF or a connection reset.
	errMsg := err.Error()
	assert.True(t,
		strings.Contains(errMsg, "failed to read response") ||
			strings.Contains(errMsg, "failed to send request"),
		"unexpected error: %s", errMsg,
	)
}

func TestGetTrackerIssue_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		require.Len(t, req.Args, 2)
		assert.Equal(t, "tracker-issue", req.Args[0])
		// The request must carry the instance name AND kind so the daemon
		// resolves the exact tracker: numeric keys are ambiguous across kinds,
		// and the same name can appear in several provider sections.
		var detailReq IssueDetailRequest
		require.NoError(t, json.Unmarshal([]byte(req.Args[1]), &detailReq))
		assert.Equal(t, "human", detailReq.Tracker)
		assert.Equal(t, "shortcut", detailReq.Kind)
		assert.Equal(t, "188", detailReq.Key)
		return Response{Stdout: `{"key":"188","title":"Building column","assignee":"Stephan","description":"Full body","description_html":"<p>Full body</p>"}` + "\n"}
	})

	issue, err := newTestClient(addr, "tok").GetTrackerIssue("shortcut", "human", "188")
	require.NoError(t, err)
	assert.Equal(t, "188", issue.Key)
	assert.Equal(t, "Stephan", issue.Assignee)
	assert.Equal(t, "Full body", issue.Description)
	assert.Equal(t, "<p>Full body</p>", issue.DescriptionHTML)
}

func TestGetTrackerIssue_InvalidJSON(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "not json\n"}
	})

	_, err := newTestClient(addr, "tok").GetTrackerIssue("shortcut", "human", "188")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid tracker issue JSON")
}
