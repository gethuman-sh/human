package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/StephanSchmidt/human/errors"
	"github.com/StephanSchmidt/human/internal/claude/hookevents"
	"github.com/StephanSchmidt/human/internal/stats"
	"github.com/StephanSchmidt/human/internal/tracker"
)

const dialTimeout = 5 * time.Second

// RunRemote connects to the daemon at addr, sends the CLI args, and returns
// the exit code. Stdout and stderr are written to os.Stdout and os.Stderr.
func RunRemote(addr, token string, args []string, version string) (int, error) {
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return 1, errors.WrapWithDetails(err, "cannot reach daemon", "addr", addr)
	}
	defer func() { _ = conn.Close() }()

	env := selectedEnv()
	cwd, _ := os.Getwd()

	req := Request{
		Version:   version,
		Token:     token,
		Args:      args,
		Env:       env,
		ClientPID: findAncestorClaude(),
		Cwd:       cwd,
	}

	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return 1, errors.WrapWithDetails(err, "failed to send request")
	}

	// Single buffered reader for the connection — creating a new
	// bufio.Reader per read would lose data buffered by the first reader.
	reader := bufio.NewReader(conn)

	line, err := reader.ReadBytes('\n')
	if err != nil {
		return 1, errors.WrapWithDetails(err, "failed to read response")
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return 1, errors.WrapWithDetails(err, "invalid response from daemon")
	}

	if resp.Stdout != "" {
		_, _ = fmt.Fprint(os.Stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		_, _ = fmt.Fprint(os.Stderr, resp.Stderr)
	}

	// Two-line OAuth protocol: daemon signals us to wait for a callback URL.
	if resp.AwaitCallback {
		return handleOAuthCallback(reader)
	}

	// Two-line destructive confirmation protocol: daemon paused a destructive
	// operation and is waiting for TUI confirmation.
	if resp.AwaitConfirm {
		return handleConfirmWait(reader, resp.ConfirmPrompt)
	}

	return resp.ExitCode, nil
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

// handleConfirmWait blocks until the daemon sends line 2 with the result of a
// destructive operation confirmation.
func handleConfirmWait(reader *bufio.Reader, prompt string) (int, error) {
	_, _ = fmt.Fprintf(os.Stderr, "Waiting for confirmation: %s\n", prompt)
	line2, err := reader.ReadBytes('\n')
	if err != nil {
		return 1, errors.WrapWithDetails(err, "failed to read confirmation response")
	}
	var resp2 Response
	if err := json.Unmarshal(line2, &resp2); err != nil {
		return 1, errors.WrapWithDetails(err, "invalid confirmation response")
	}
	if resp2.Stdout != "" {
		_, _ = fmt.Fprint(os.Stdout, resp2.Stdout)
	}
	if resp2.Stderr != "" {
		_, _ = fmt.Fprint(os.Stderr, resp2.Stderr)
	}
	return resp2.ExitCode, nil
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

// GetTrackerIssues fetches open issues from all configured tracker projects via the daemon.
func GetTrackerIssues(addr, token string) ([]TrackerIssuesResult, error) {
	out, err := RunRemoteCapture(addr, token, []string{"tracker-issues"})
	if err != nil {
		return nil, err
	}
	var results []TrackerIssuesResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, errors.WrapWithDetails(err, "invalid tracker issues JSON")
	}
	return results, nil
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
		for _, line := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(line, "PPid:") {
				ppid, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
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
	keys := []string{"NO_COLOR", "TERM", "COLUMNS"}
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
