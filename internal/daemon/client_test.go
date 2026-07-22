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

func TestRunRemote_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, "test-token", req.Token)
		assert.Equal(t, []string{"echo", "hello"}, req.Args)
		return Response{
			Stdout:   "hello\n",
			ExitCode: 0,
		}
	})

	exitCode, err := RunRemote(addr, "test-token", []string{"echo", "hello"}, "dev")
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

	exitCode, err := RunRemote(addr, "tok", []string{"fail"}, "dev")
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

	events, err := GetNetworkEvents(addr, "tok")
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

	events, err := GetNetworkEvents(addr, "tok")
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestGetNetworkEvents_InvalidJSON(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "not json\n"}
	})

	_, err := GetNetworkEvents(addr, "tok")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid network events JSON")
}

func TestRunRemote_ConnectionRefused(t *testing.T) {
	exitCode, err := RunRemote("127.0.0.1:1", "tok", []string{"echo"}, "dev")
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

	st, err := GetIdeationStatus(addr, "tok")
	require.NoError(t, err)
	assert.Equal(t, "ideation-1", st.SessionID)
	assert.Equal(t, IdeationAwaitingReply, st.State)
}

func TestIdeationStart_ErrorPropagates(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stderr: "ideation not available\n", ExitCode: 1}
	})

	_, err := IdeationStart(addr, "tok", IdeationStartRequest{Seed: "idea"})
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

	st, err := IdeationReply(addr, "tok", IdeationReplyRequest{SessionID: "ideation-1", Message: "answer"})
	require.NoError(t, err)
	assert.Equal(t, IdeationThinking, st.State)
}

func TestGetIdeationStatus_InvalidJSON(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "not json\n"}
	})

	_, err := GetIdeationStatus(addr, "tok")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ideation status JSON")
}

func TestRunRemote_VersionForwarded(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, "1.2.3", req.Version)
		return Response{ExitCode: 0}
	})

	exitCode, err := RunRemote(addr, "tok", []string{}, "1.2.3")
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestRunRemote_EnvForwarded(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, "1", req.Env["NO_COLOR"])
		return Response{ExitCode: 0}
	})

	exitCode, err := RunRemote(addr, "tok", []string{}, "dev")
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestRunRemote_ClientPIDForwarded(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Greater(t, req.ClientPID, 0, "ClientPID should be set to parent PID")
		return Response{ExitCode: 0}
	})

	exitCode, err := RunRemote(addr, "tok", []string{}, "dev")
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestGetLogMode_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"log-mode"}, req.Args)
		return Response{Stdout: "full\n"}
	})

	mode, err := GetLogMode(addr, "tok")
	require.NoError(t, err)
	assert.Equal(t, "full", mode)
}

func TestSetLogMode_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"log-mode", "off"}, req.Args)
		return Response{Stdout: "off\n"}
	})

	mode, err := SetLogMode(addr, "tok", "off")
	require.NoError(t, err)
	assert.Equal(t, "off", mode)
}

func TestGetHookSnapshot_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"hook-snapshot"}, req.Args)
		return Response{Stdout: `{"session-1":{"session_id":"session-1","cwd":"/proj","status":1}}` + "\n"}
	})

	snap, err := GetHookSnapshot(addr, "tok")
	require.NoError(t, err)
	require.Contains(t, snap, "session-1")
	assert.Equal(t, "/proj", snap["session-1"].Cwd)
}

func TestGetHookSnapshot_InvalidJSON(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{Stdout: "bad json\n"}
	})

	_, err := GetHookSnapshot(addr, "tok")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid hook snapshot JSON")
}

func TestGetTrackerDiagnose_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"tracker-diagnose"}, req.Args)
		return Response{Stdout: `[{"name":"jira","ok":true}]` + "\n"}
	})

	statuses, err := GetTrackerDiagnose(addr, "tok")
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, "jira", statuses[0].Name)
}

func TestGetTrackerIssues_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"tracker-issues"}, req.Args)
		return Response{Stdout: `[{"tracker_name":"jira","tracker_kind":"jira","project":"PROJ","issues":[]}]` + "\n"}
	})

	results, err := GetTrackerIssues(addr, "tok")
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

	results, err := GetTrackerIssuesLite(addr, "tok")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "jira", results[0].TrackerName)
}

func TestGetPendingConfirms_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"pending-confirms"}, req.Args)
		return Response{Stdout: `[{"id":"abc","prompt":"delete?"}]` + "\n"}
	})

	confirms, err := GetPendingConfirms(addr, "tok")
	require.NoError(t, err)
	require.Len(t, confirms, 1)
	assert.Equal(t, "abc", confirms[0].ID)
}

func TestGetToolStats_Success(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"tool-stats"}, req.Args)
		return Response{Stdout: `{"total_events":5,"by_tool":[],"by_event_name":[],"by_hour":[]}` + "\n"}
	})

	ts, err := GetToolStats(addr, "tok")
	require.NoError(t, err)
	assert.Equal(t, 5, ts.TotalEvents)
}

func TestSendConfirmDecision_Approved(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"confirm-op", "abc", "yes"}, req.Args)
		return Response{ExitCode: 0}
	})

	err := SendConfirmDecision(addr, "tok", "abc", true)
	require.NoError(t, err)
}

func TestSendConfirmDecision_Denied(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"confirm-op", "abc", "no"}, req.Args)
		return Response{ExitCode: 0}
	})

	err := SendConfirmDecision(addr, "tok", "abc", false)
	require.NoError(t, err)
}

func TestStartFindbugs_SendsRoute(t *testing.T) {
	addr := startMockDaemon(t, func(req Request) Response {
		assert.Equal(t, []string{"findbugs-start"}, req.Args)
		return Response{ExitCode: 0, Stdout: "ok\n"}
	})

	err := StartFindbugs(addr, "tok")
	require.NoError(t, err)
}

func TestStartFindbugs_ErrorPropagates(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{ExitCode: 1, Stderr: "findbugs sweep not available"}
	})

	err := StartFindbugs(addr, "tok")
	require.Error(t, err)
}

func TestRunRemoteCapture_DaemonError(t *testing.T) {
	addr := startMockDaemon(t, func(_ Request) Response {
		return Response{ExitCode: 1, Stderr: "some error"}
	})

	_, err := RunRemoteCapture(addr, "tok", []string{"bad-cmd"})
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
	code, err := RunRemote(addr, "tok", []string{"jira", "issue", "delete", "KAN-1"}, "dev")
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
	st, err := GetConfirmStatus(addr, "tok", "c-9")
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

	exitCode, err := RunRemote(ln.Addr().String(), "tok", []string{}, "dev")
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

	issue, err := GetTrackerIssue(addr, "tok", "shortcut", "human", "188")
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

	_, err := GetTrackerIssue(addr, "tok", "shortcut", "human", "188")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid tracker issue JSON")
}
