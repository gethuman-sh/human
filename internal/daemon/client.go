package daemon

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/audit"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/settings"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
)

const dialTimeout = 5 * time.Second

// ClientVersion is stamped into every request this process sends so the
// daemon can reject protocol-stale clients up front (see the server's
// version gate). The CLI overwrites it at startup with its build version;
// the "dev" default keeps same-tree builds (tests, desktop app) passing.
var ClientVersion = "dev"

// RunRemote connects to the daemon at addr, sends the CLI args, and returns
// the exit code. Stdout and stderr are written to os.Stdout and os.Stderr.
//
// Destructive commands run a grant cycle with NO waiting: the daemon queues a
// permission request and answers await_confirm; we print instructions and
// return immediately. There is deliberately no countdown — the user approves
// whenever, and re-running the command redeems the grant (matched by
// operation, so a fresh process needs no remembered ID). Denials are answered
// on the re-run with a back-off instruction.
func RunRemote(addr, token string, args []string, version string) (int, error) {
	code, resp, err := runRemoteOnce(addr, token, args, version, newConfirmID())
	if err != nil || !resp.AwaitConfirm {
		return code, err
	}
	_, _ = fmt.Fprintf(os.Stderr,
		"Permission required: %s (id %s)\nApprove or deny it in the human desktop app or TUI — there is no time limit. Once approved, re-run this exact command to execute it (it will not ask again). Check the decision anytime with: human confirm-status %s\n",
		resp.ConfirmPrompt, resp.ConfirmID, resp.ConfirmID)
	return 1, nil
}

// runRemoteOnce performs a single request/response round-trip. The returned
// Response carries the await_confirm signal for RunRemote's grant cycle.
func runRemoteOnce(addr, token string, args []string, version, confirmID string) (int, Response, error) {
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return 1, Response{}, errors.WrapWithDetails(err, "cannot reach daemon", "addr", addr)
	}
	defer func() { _ = conn.Close() }()

	env := selectedEnv()
	cwd, _ := os.Getwd()

	req := Request{
		Version:   version,
		Protocol:  Protocol,
		Token:     token,
		Args:      args,
		Env:       env,
		ClientPID: findAncestorClaude(),
		Cwd:       cwd,
		ConfirmID: confirmID,
	}

	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return 1, Response{}, errors.WrapWithDetails(err, "failed to send request")
	}

	// Single buffered reader for the connection — creating a new
	// bufio.Reader per read would lose data buffered by the first reader.
	reader := bufio.NewReader(conn)

	line, err := reader.ReadBytes('\n')
	if err != nil {
		return 1, Response{}, errors.WrapWithDetails(err, "failed to read response")
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return 1, Response{}, errors.WrapWithDetails(err, "invalid response from daemon")
	}

	if resp.Stdout != "" {
		_, _ = fmt.Fprint(os.Stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		_, _ = fmt.Fprint(os.Stderr, resp.Stderr)
	}

	// Two-line OAuth protocol: daemon signals us to wait for a callback URL.
	if resp.AwaitCallback {
		code, err := handleOAuthCallback(reader)
		return code, resp, err
	}

	return resp.ExitCode, resp, nil
}

// handleOAuthCallback reads line 2 of the OAuth relay protocol and delivers
// the callback URL. Claude Code awaits the BROWSER process exit (10-min timeout
// via execa), so we stay alive, read the callback URL, deliver it, then exit 0.
func handleOAuthCallback(reader *bufio.Reader) (int, error) {
	line2, err := reader.ReadBytes('\n')
	if err != nil {
		return 1, errors.WrapWithDetails(err, "failed to read callback response")
	}
	var resp2 Response
	if err := json.Unmarshal(line2, &resp2); err != nil {
		return 1, errors.WrapWithDetails(err, "invalid callback response")
	}
	if resp2.Callback != "" {
		if err := deliverCallback(resp2.Callback); err != nil {
			return 1, errors.WrapWithDetails(err, "failed to deliver OAuth callback")
		}
	}
	return 0, nil
}

// newConfirmID generates a client-side unique ID for a potentially
// destructive request. Client-generated so a resubmit with the same ID is
// idempotent on the daemon and the client knows the key to poll even if the
// response line is lost.
func newConfirmID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// Entropy failure — fall back to a time-based ID; uniqueness per
		// client process is all that is required here, not secrecy.
		return fmt.Sprintf("c-%d", time.Now().UnixNano())
	}
	return "c-" + hex.EncodeToString(buf)
}

// GetConfirmStatus fetches the decision state of a queued destructive
// operation by ID.
func GetConfirmStatus(addr, token, id string) (ConfirmStatus, error) {
	out, err := RunRemoteCapture(addr, token, []string{"confirm-status", id})
	if err != nil {
		return ConfirmStatus{}, err
	}
	var st ConfirmStatus
	if err := json.Unmarshal(out, &st); err != nil {
		return ConfirmStatus{}, errors.WrapWithDetails(err, "invalid confirm status JSON")
	}
	return st, nil
}

// RunRemoteCapture connects to the daemon and runs args, returning stdout
// as bytes instead of printing to os.Stdout.
func RunRemoteCapture(addr, token string, args []string) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "cannot reach daemon", "addr", addr)
	}
	defer func() { _ = conn.Close() }()

	cwd, _ := os.Getwd()
	req := Request{
		Version:   ClientVersion,
		Protocol:  Protocol,
		Token:     token,
		Args:      args,
		ClientPID: os.Getpid(),
		Cwd:       cwd,
	}

	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return nil, errors.WrapWithDetails(err, "failed to send request")
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, errors.WrapWithDetails(err, "failed to read response")
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid response from daemon")
	}

	if resp.ExitCode != 0 {
		return nil, errors.WithDetails("daemon command failed", "stderr", resp.Stderr)
	}

	return []byte(resp.Stdout), nil
}

// QueryAudit reads audit events from the daemon (which owns the audit DB),
// forwarding the pre-parsed filter flags. filterArgs is the slice of
// --since/--until/--subject/--tracker/--limit tokens.
func QueryAudit(addr, token string, filterArgs []string) ([]audit.Event, error) {
	out, err := RunRemoteCapture(addr, token, append([]string{"audit-query"}, filterArgs...))
	if err != nil {
		return nil, err
	}
	var events []audit.Event
	if err := json.Unmarshal(out, &events); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid audit events JSON")
	}
	return events, nil
}

// GetLogMode fetches the current traffic log mode from the daemon.
func GetLogMode(addr, token string) (string, error) {
	out, err := RunRemoteCapture(addr, token, []string{"log-mode"})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SetLogMode sets the traffic log mode on the daemon. Returns the new mode.
func SetLogMode(addr, token, mode string) (string, error) {
	out, err := RunRemoteCapture(addr, token, []string{"log-mode", mode})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GetHookSnapshot fetches the current per-session hook state from the daemon.
func GetHookSnapshot(addr, token string) (map[string]hookevents.SessionSnapshot, error) {
	out, err := RunRemoteCapture(addr, token, []string{"hook-snapshot"})
	if err != nil {
		return nil, err
	}
	var snap map[string]hookevents.SessionSnapshot
	if err := json.Unmarshal(out, &snap); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid hook snapshot JSON")
	}
	return snap, nil
}

// GetNetworkEvents fetches the current ambient network activity buffer
// from the daemon. Returns a nil slice (not a nil error) when the daemon
// replies with an empty list so the TUI can collapse the panel.
func GetNetworkEvents(addr, token string) ([]NetworkEvent, error) {
	out, err := RunRemoteCapture(addr, token, []string{"network-events"})
	if err != nil {
		return nil, err
	}
	var events []NetworkEvent
	if err := json.Unmarshal(out, &events); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid network events JSON")
	}
	return events, nil
}

// GetTrackerDiagnose fetches tracker credential status from the daemon.
func GetTrackerDiagnose(addr, token string) ([]tracker.TrackerStatus, error) {
	out, err := RunRemoteCapture(addr, token, []string{"tracker-diagnose"})
	if err != nil {
		return nil, err
	}
	var statuses []tracker.TrackerStatus
	if err := json.Unmarshal(out, &statuses); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid tracker diagnose JSON")
	}
	return statuses, nil
}

// GetConfig fetches the masked settings snapshot for the caller's project.
// Values arrive with vault references verbatim and literal secrets masked.
func GetConfig(addr, token string) (settings.Doc, error) {
	out, err := RunRemoteCapture(addr, token, []string{"config-get"})
	if err != nil {
		return settings.Doc{}, err
	}
	var doc settings.Doc
	if err := json.Unmarshal(out, &doc); err != nil {
		return settings.Doc{}, errors.WrapWithDetails(err, "invalid config JSON")
	}
	return doc, nil
}

// SetConfig writes one settings key and returns the refreshed snapshot so
// callers can re-render without a second round trip.
func SetConfig(addr, token string, req SetConfigRequest) (settings.Doc, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return settings.Doc{}, errors.WrapWithDetails(err, "marshaling config-set request")
	}
	out, err := RunRemoteCapture(addr, token, []string{"config-set", string(data)})
	if err != nil {
		return settings.Doc{}, err
	}
	var doc settings.Doc
	if err := json.Unmarshal(out, &doc); err != nil {
		return settings.Doc{}, errors.WrapWithDetails(err, "invalid config JSON")
	}
	return doc, nil
}

// GetTrackerIssues fetches open issues from all configured tracker projects via the daemon.
func GetTrackerIssues(addr, token string) ([]TrackerIssuesResult, error) {
	return getTrackerIssues(addr, token, "tracker-issues")
}

// GetTrackerIssuesLite fetches issue titles only, skipping the per-ticket comment
// scan that derives board stages. It returns quickly so the board can render
// titles before the full GetTrackerIssues reconcile completes.
func GetTrackerIssuesLite(addr, token string) ([]TrackerIssuesResult, error) {
	return getTrackerIssues(addr, token, "tracker-issues-lite")
}

// GetTrackerIssue fetches one full issue by key via the daemon, including the
// daemon-rendered sanitized HTML of its description. The board's detail panel
// uses it because list fetches on some trackers return slim payloads without
// descriptions. trackerKind+trackerName pin the instance the issue was listed
// from: keys are ambiguous across kinds and names can repeat across provider
// sections.
func GetTrackerIssue(addr, token, trackerKind, trackerName, key string) (*IssueDetailResult, error) {
	data, err := json.Marshal(IssueDetailRequest{Tracker: trackerName, Kind: trackerKind, Key: key})
	if err != nil {
		return nil, errors.WrapWithDetails(err, "marshaling issue detail request")
	}
	out, err := RunRemoteCapture(addr, token, []string{"tracker-issue", string(data)})
	if err != nil {
		return nil, err
	}
	var result IssueDetailResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid tracker issue JSON")
	}
	return &result, nil
}

func getTrackerIssues(addr, token, command string) ([]TrackerIssuesResult, error) {
	out, err := RunRemoteCapture(addr, token, []string{command})
	if err != nil {
		return nil, err
	}
	var results []TrackerIssuesResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid tracker issues JSON")
	}
	return results, nil
}

// BoardTransition asks the daemon to advance a card one pipeline stage. The
// request is sent as a single JSON arg so multi-word PM titles survive arg
// splitting on the daemon side.
func BoardTransition(addr, token string, req BoardTransitionRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling board transition request")
	}
	_, err = RunRemoteCapture(addr, token, []string{"board-transition", string(data)})
	return err
}

// BoardFix asks the daemon to launch the autonomous bug-fix pipeline
// (/human-autofix) on a bug ticket. The request is a single JSON arg, matching
// BoardTransition; it returns once the agent is launched, not when the fix
// finishes.
func BoardFix(addr, token string, req BoardFixRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling board fix request")
	}
	_, err = RunRemoteCapture(addr, token, []string{"board-fix", string(data)})
	return err
}

// SendBoardOption records a chosen option from a card's open decision block and
// relaunches the block's stage with the choice. Single JSON arg, matching
// BoardTransition; returns once the agent is launched.
func SendBoardOption(addr, token string, req BoardOptionRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling board option request")
	}
	_, err = RunRemoteCapture(addr, token, []string{"board-option", string(data)})
	return err
}

// GenerateFeatures asks the daemon to launch the human-features skill, which
// regenerates FEATURE.json for the registered project. It takes no arguments —
// the daemon resolves the project directory itself — and returns once the agent
// is launched, not when generation finishes.
func GenerateFeatures(addr, token string) error {
	_, err := RunRemoteCapture(addr, token, []string{"features-generate"})
	return err
}

// CloseTicket asks the daemon to close a PM ticket (transition it to Done). The
// request is a single JSON arg, matching BoardTransition. This is a dedicated
// route, so it never hits the interactive `issue status` confirmation.
func CloseTicket(addr, token string, req CloseTicketRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling close ticket request")
	}
	_, err = RunRemoteCapture(addr, token, []string{"close-ticket", string(data)})
	return err
}

// CreateMocks asks the daemon to launch the human-mockups skill for one PM
// ticket. The request is a single JSON arg, matching BoardTransition. It
// returns once the agent is launched, not when generation finishes — the
// board learns about the finished set from the mockup link file on the next
// card refresh.
func CreateMocks(addr, token string, req CreateMocksRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling create mocks request")
	}
	_, err = RunRemoteCapture(addr, token, []string{"create-mocks", string(data)})
	return err
}

// IdeationStart starts (or re-attaches to) the board ideation session.
func IdeationStart(addr, token string, req IdeationStartRequest) (IdeationStatus, error) {
	return ideationCall(addr, token, "ideation-start", req)
}

// IdeationReply sends the user's answer into the running ideation session.
func IdeationReply(addr, token string, req IdeationReplyRequest) (IdeationStatus, error) {
	return ideationCall(addr, token, "ideation-reply", req)
}

// IdeationApprove submits the user's (possibly edited) guided-mode draft for
// ticket creation.
func IdeationApprove(addr, token string, req IdeationApproveRequest) (IdeationStatus, error) {
	return ideationCall(addr, token, "ideation-approve", req)
}

// IdeaCreate quick-captures a title-only, idea-labeled ticket on the PM
// tracker — the Ideas column's `+` button.
func IdeaCreate(addr, token string, req IdeaCreateRequest) (IdeaCreateResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return IdeaCreateResponse{}, errors.WrapWithDetails(err, "marshaling idea-create request")
	}
	out, err := RunRemoteCapture(addr, token, []string{"idea-create", string(data)})
	if err != nil {
		return IdeaCreateResponse{}, err
	}
	var resp IdeaCreateResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return IdeaCreateResponse{}, errors.WrapWithDetails(err, "invalid idea-create JSON")
	}
	return resp, nil
}

// BugCreate files a defect ticket on the PM tracker — the Bugs pane's `+`
// dialog (title plus free-text description).
func BugCreate(addr, token string, req BugCreateRequest) (BugCreateResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return BugCreateResponse{}, errors.WrapWithDetails(err, "marshaling bug-create request")
	}
	out, err := RunRemoteCapture(addr, token, []string{"bug-create", string(data)})
	if err != nil {
		return BugCreateResponse{}, err
	}
	var resp BugCreateResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return BugCreateResponse{}, errors.WrapWithDetails(err, "invalid bug-create JSON")
	}
	return resp, nil
}

// GetIdeationStatus fetches the current ideation session snapshot.
func GetIdeationStatus(addr, token string) (IdeationStatus, error) {
	out, err := RunRemoteCapture(addr, token, []string{"ideation-status"})
	if err != nil {
		return IdeationStatus{}, err
	}
	var st IdeationStatus
	if err := json.Unmarshal(out, &st); err != nil {
		return IdeationStatus{}, errors.WrapWithDetails(err, "invalid ideation status JSON")
	}
	return st, nil
}

// ideationCall marshals payload as the single JSON arg and decodes the returned
// snapshot — the same wire shape as BoardTransition, with a JSON reply.
func ideationCall(addr, token, route string, payload any) (IdeationStatus, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return IdeationStatus{}, errors.WrapWithDetails(err, "marshaling "+route+" request")
	}
	out, err := RunRemoteCapture(addr, token, []string{route, string(data)})
	if err != nil {
		return IdeationStatus{}, err
	}
	var st IdeationStatus
	if err := json.Unmarshal(out, &st); err != nil {
		return IdeationStatus{}, errors.WrapWithDetails(err, "invalid ideation status JSON")
	}
	return st, nil
}

// GetPendingConfirms fetches pending destructive operation confirmations from the daemon.
func GetPendingConfirms(addr, token string) ([]PendingConfirm, error) {
	out, err := RunRemoteCapture(addr, token, []string{"pending-confirms"})
	if err != nil {
		return nil, err
	}
	var results []PendingConfirm
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid pending confirms JSON")
	}
	return results, nil
}

// GetDoctor fetches the daemon's substrate health checks. refresh forces a
// live run instead of the poller cache.
func GetDoctor(addr, token string, refresh bool) (DoctorData, error) {
	args := []string{"doctor"}
	if refresh {
		args = append(args, "refresh")
	}
	out, err := RunRemoteCapture(addr, token, args)
	if err != nil {
		return DoctorData{}, err
	}
	var data DoctorData
	if err := json.Unmarshal(out, &data); err != nil {
		return DoctorData{}, errors.WrapWithDetails(err, "invalid doctor JSON")
	}
	return data, nil
}

// GetToolStats fetches pre-aggregated tool call statistics from the daemon.
func GetToolStats(addr, token string) (*stats.ToolStats, error) {
	out, err := RunRemoteCapture(addr, token, []string{"tool-stats"})
	if err != nil {
		return nil, err
	}
	var ts stats.ToolStats
	if err := json.Unmarshal(out, &ts); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid tool stats JSON")
	}
	return &ts, nil
}

// GetStatsOverview fetches the consolidated board-stats payload for a range
// ("24h" | "7d" | "30d") from the daemon.
func GetStatsOverview(addr, token, rng string) (*StatsOverview, error) {
	out, err := RunRemoteCapture(addr, token, []string{"stats-overview", "--range", rng})
	if err != nil {
		return nil, err
	}
	var ov StatsOverview
	if err := json.Unmarshal(out, &ov); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid stats overview JSON")
	}
	return &ov, nil
}

// SendConfirmDecision sends a confirmation decision for a pending destructive operation.
func SendConfirmDecision(addr, token, id string, approved bool) error {
	decision := "no"
	if approved {
		decision = "yes"
	}
	_, err := RunRemoteCapture(addr, token, []string{"confirm-op", id, decision})
	return err
}

// Subscribe opens a persistent connection to the daemon's subscribe endpoint.
// It returns a channel that receives a signal each time the daemon's state
// changes, and a cleanup function that closes the connection.
// The channel is closed when the connection drops or cleanup is called.
func Subscribe(addr, token string) (<-chan SubscribeEvent, func(), error) {
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, nil, errors.WrapWithDetails(err, "cannot reach daemon", "addr", addr)
	}

	cwd, _ := os.Getwd()
	req := Request{
		Version:   ClientVersion,
		Protocol:  Protocol,
		Token:     token,
		Args:      []string{"subscribe"},
		ClientPID: os.Getpid(),
		Cwd:       cwd,
	}
	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		_ = conn.Close()
		return nil, nil, errors.WrapWithDetails(err, "failed to send subscribe request")
	}

	ch := make(chan SubscribeEvent, 1)
	go func() {
		defer close(ch)
		reader := bufio.NewReader(conn)
		for {
			line, readErr := reader.ReadBytes('\n')
			if readErr != nil {
				return
			}
			var evt SubscribeEvent
			if json.Unmarshal(line, &evt) == nil {
				select {
				case ch <- evt:
				default: // coalesce if TUI hasn't consumed yet
				}
			}
		}
	}()

	cleanup := func() { _ = conn.Close() }
	return ch, cleanup, nil
}

// deliverCallback performs an HTTP GET to the callback URL, delivering the
// OAuth callback to the local listener (e.g. Claude Code) from inside the
// container where localhost is reachable.
func deliverCallback(callbackURL string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	httpResp, err := client.Get(callbackURL) //nolint:gosec // URL is from trusted daemon
	if err != nil {
		return err
	}
	if httpResp == nil {
		return errors.WithDetails("OAuth callback delivery returned nil response")
	}
	if httpResp.Body != nil {
		defer func() { _ = httpResp.Body.Close() }()
		_, _ = io.Copy(io.Discard, httpResp.Body)
	}
	if httpResp.StatusCode >= http.StatusBadRequest {
		return errors.WithDetails("OAuth callback delivery failed", "statusCode", httpResp.StatusCode)
	}
	return nil
}

// findAncestorClaude walks the process tree from the current process upward,
// looking for an ancestor whose /proc/<pid>/comm is "claude". Returns the
// first matching PID, or falls back to os.Getppid() if none is found.
func findAncestorClaude() int {
	pid := os.Getppid()
	for pid > 1 {
		comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			break
		}
		if strings.TrimSpace(string(comm)) == "claude" {
			return pid
		}
		// Read the parent PID from /proc/<pid>/status.
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			break
		}
		ppid := 0
		for line := range strings.SplitSeq(string(status), "\n") {
			if after, ok := strings.CutPrefix(line, "PPid:"); ok {
				ppid, _ = strconv.Atoi(strings.TrimSpace(after))
				break
			}
		}
		if ppid <= 1 || ppid == pid {
			break
		}
		pid = ppid
	}
	return os.Getppid()
}

// selectedEnv returns a small set of display-related env vars to forward.
func selectedEnv() map[string]string {
	keys := []string{
		"NO_COLOR", "TERM", "COLUMNS",
		// Forward the at-decision-time audit context so the daemon can record
		// the agent's model and rationale alongside the action it mediates.
		"HUMAN_AUDIT_MODEL_ID", "HUMAN_AUDIT_MODEL_VERSION",
		"HUMAN_AUDIT_INPUTS", "HUMAN_AUDIT_RATIONALE",
	}
	env := make(map[string]string)
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}
