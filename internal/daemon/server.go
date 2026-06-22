package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/audit"
	"github.com/gethuman-sh/human/internal/browser"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/cliflags"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/env"
	"github.com/gethuman-sh/human/internal/proxy"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

// defaultBrowserOpener wraps browser.DefaultOpener for production use.
type defaultBrowserOpener struct{}

func (defaultBrowserOpener) Open(url string) error {
	return browser.DefaultOpener{}.Open(url)
}

// Server listens for incoming client connections and executes CLI commands.
type Server struct {
	Addr             string
	Token            string
	SafeMode         bool
	CmdFactory       func() *cobra.Command
	Opener           BrowserOpener // used for OAuth relay; defaults to browser.DefaultOpener
	Logger           zerolog.Logger
	ConnectedPIDs    *ConnectedTracker                        // tracks client PIDs that have pinged; nil disables tracking
	HookEvents       *HookEventStore                          // in-memory hook event buffer; nil disables hook event tracking
	NetworkEvents    *NetworkEventStore                       // in-memory ambient network activity buffer; nil disables
	IssueFetcher     func() ([]TrackerIssuesResult, error)    // injected; fetches issues from configured trackers
	TrackerDiagnoser func(dir string) []tracker.TrackerStatus // injected; diagnoses tracker status with vault resolution
	Projects         *ProjectRegistry                         // multi-project routing; nil means single-project mode
	PendingConfirms  *PendingConfirmStore                     // pending destructive operation confirmations; nil disables
	StatsWriter      *stats.Writer                            // async SQLite writer for tool event persistence; nil disables
	StatsStore       *stats.StatsStore                        // for query-time aggregation; nil disables tool-stats route
	AuditSink        *audit.Writer                            // records mutating tracker actions for the audit trail; nil disables
	AuditStore       *audit.Store                             // serves audit-query reads; nil disables audit-query route
	AgentCleaner     AgentCleaner                             // async agent cleanup; nil disables agent-stop-async route
	VaultResolver    *vault.Resolver                          // session-scoped vault resolver; reused across requests to avoid repeated op.exe calls

	wg sync.WaitGroup // tracks in-flight handler goroutines for graceful shutdown

	// shutdown fires when the server is stopping. Set once at the start of
	// ListenAndServe (before any connection is accepted) and only read by
	// handlers, so no additional synchronization is needed. Long-lived
	// handlers (e.g. subscribe) select on it so they don't pin s.wg.Wait().
	shutdown <-chan struct{}
}

// ListenAndServe starts the TCP listener and blocks until ctx is cancelled.
// On shutdown it waits for all in-flight handler goroutines to return before
// closing, so a client request that's already accepted is never torn down
// mid-flight by listener close alone.
func (s *Server) ListenAndServe(ctx context.Context) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", s.Addr)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()

	s.shutdown = ctx.Done()

	s.Logger.Info().Str("addr", ln.Addr().String()).Msg("daemon listening")

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	// Limit concurrent connections to prevent resource exhaustion.
	const maxConns = 64
	sem := make(chan struct{}, maxConns)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				// Wait for any in-flight handlers before returning so the
				// caller observes a fully-quiesced server.
				s.wg.Wait()
				return nil
			}
			s.Logger.Warn().Err(err).Msg("accept error")
			continue
		}
		select {
		case sem <- struct{}{}:
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer func() { <-sem }()
				s.handleConn(conn)
			}()
		default:
			s.Logger.Warn().Msg("connection limit reached, rejecting")
			if conn != nil {
				_ = conn.Close()
			}
		}
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	// Bound the time and size of the request line. The deadline must be
	// applied to the raw conn BEFORE the bufio.Reader is created so the
	// underlying read inherits it; the LimitReader caps the request to
	// 1 MiB so a malicious client can't OOM the daemon by streaming an
	// unbounded JSON line.
	const maxRequestBytes = 1 << 20
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	limited := io.LimitReader(conn, maxRequestBytes)
	reader := bufio.NewReader(limited)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		s.writeError(conn, "failed to read request", 1)
		return
	}
	// Clear the deadline once the request is parsed; the rest of the
	// handler runs long-lived operations that must not inherit it.
	_ = conn.SetReadDeadline(time.Time{})

	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeError(conn, "invalid request JSON", 1)
		return
	}

	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.Token)) != 1 {
		s.writeError(conn, "authentication failed: invalid token", 1)
		return
	}

	if req.ClientPID > 0 && s.ConnectedPIDs != nil {
		s.ConnectedPIDs.Touch(req.ClientPID)
	}

	// Resolve project directory for this request.
	projectDir, err := s.resolveProjectDir(req.Cwd)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}

	s.Logger.Info().Strs("args", req.Args).Str("project_dir", projectDir).Msg("handling request")

	if s.routeIntercept(conn, reader, req.Args, projectDir, req.ClientPID) {
		return
	}

	// Intercept destructive operations for interactive confirmation.
	if op, ok := detectDestructive(req.Args); ok && s.PendingConfirms != nil {
		s.handleDestructiveConfirm(conn, req, op, projectDir)
		return
	}

	// Apply client environment variables and execute the command.
	s.executeCommand(conn, req, projectDir)
}

// executeCommand applies env vars (including safe mode) and runs the CLI command.
//
// Per-request env values are carried on the cobra command's context via
// env.WithEnv. This avoids any os.Setenv mutation, so concurrent requests
// no longer fight for a process-wide environment lock and a request can
// never observe another request's env values.
func (s *Server) executeCommand(conn net.Conn, req Request, projectDir string) {
	// Safe mode is enforced via context-bound env so clients cannot
	// override it via flag injection (e.g. --safe=false).
	if s.SafeMode {
		if req.Env == nil {
			req.Env = make(map[string]string)
		}
		req.Env["HUMAN_SAFE_MODE"] = "1"
	}
	if projectDir != "." {
		if req.Env == nil {
			req.Env = make(map[string]string)
		}
		req.Env["HUMAN_PROJECT_DIR"] = projectDir
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := s.CmdFactory()
	cmd.SetArgs(req.Args)
	cmd.SetOut(&stdoutBuf)
	cmd.SetErr(&stderrBuf)
	ctx := env.WithEnv(context.Background(), req.Env)
	ctx = vault.WithResolver(ctx, s.VaultResolver)
	cmd.SetContext(ctx)

	exitCode := 0
	if err := cmd.Execute(); err != nil {
		exitCode = 1
	}

	// Emit the audit event after execution so the outcome (and the per-request
	// HUMAN_AUDIT_* decision context on ctx) is known.
	outcome := audit.OutcomeSuccess
	if exitCode != 0 {
		outcome = audit.OutcomeFailure
	}
	s.emitAudit(req.Args, outcome, func(k string) string { return env.Lookup(ctx, k) })

	resp := Response{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
	}

	enc := json.NewEncoder(conn)
	if err := enc.Encode(resp); err != nil {
		s.Logger.Warn().Err(err).Msg("failed to write response")
	}
}

// emitAudit records a mutating command's outcome. It is a no-op when the
// command is not mutating or no sink is configured. lookup resolves the
// per-request HUMAN_AUDIT_* decision context.
func (s *Server) emitAudit(args []string, outcome audit.Outcome, lookup func(string) string) {
	if s.AuditSink == nil {
		return
	}
	op, ok := audit.DetectMutating(args)
	if !ok {
		return
	}
	dc := audit.DecisionFromEnv(lookup)
	e, err := audit.BuildEvent(time.Now().UTC(), op, outcome, dc, args)
	if err != nil {
		s.Logger.Warn().Err(err).Msg("audit event build failed")
		return
	}
	s.AuditSink.Send(e)
}

// handleLogMode handles get/set of the traffic log mode in-memory.
// No args → return current mode. One arg → set and return new mode.
func (s *Server) handleLogMode(conn net.Conn, args []string) {
	if len(args) == 0 {
		// Get current mode.
		mode := proxy.GetLogMode()
		resp := Response{Stdout: proxy.LogModeString(mode) + "\n"}
		enc := json.NewEncoder(conn)
		_ = enc.Encode(resp)
		return
	}

	mode, err := proxy.ParseLogMode(args[0])
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}

	proxy.SetLogMode(mode)
	s.Logger.Info().Str("mode", proxy.LogModeString(mode)).Msg("traffic log mode changed")

	resp := Response{Stdout: proxy.LogModeString(mode) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// routeDataStores handles read routes backed by the daemon's SQLite stores
// (tool-stats, audit-query). Split out of routeIntercept so each switch stays
// small and the analytics/query reads live together. Returns true if handled.
func (s *Server) routeDataStores(conn net.Conn, args []string) bool {
	switch args[0] {
	case "tool-stats":
		s.handleToolStats(conn)
		return true
	case "audit-query":
		s.handleAuditQuery(conn, args[1:])
		return true
	}
	return false
}

// routeIntercept handles special commands that don't need subprocess execution.
// projectDir is the resolved project directory for this request.
// clientPID identifies the requesting client for authorization checks.
// Returns true if the command was handled.
func (s *Server) routeIntercept(conn net.Conn, reader *bufio.Reader, args []string, projectDir string, clientPID int) bool {
	if len(args) == 0 {
		return false
	}
	if s.routeDataStores(conn, args) {
		return true
	}
	switch args[0] {
	case "log-mode":
		s.handleLogMode(conn, args[1:])
		return true
	case "hook-event":
		s.handleHookEvent(conn, args[1:])
		return true
	case "hook-snapshot":
		s.handleHookSnapshot(conn)
		return true
	case "network-events":
		s.handleNetworkEvents(conn)
		return true
	case "tracker-diagnose":
		s.handleTrackerDiagnose(conn, projectDir)
		return true
	case "tracker-issues":
		s.handleTrackerIssues(conn)
		return true
	case "pending-confirms":
		s.handlePendingConfirms(conn)
		return true
	case "confirm-op":
		s.handleConfirmOp(conn, args[1:], clientPID)
		return true
	case "agent-stop-async":
		s.handleAgentStopAsync(conn, args[1:])
		return true
	case "subscribe":
		s.handleSubscribe(conn)
		return true
	}

	// Intercept browser commands with OAuth redirect_uri for relay.
	if info, url := isBrowserWithRedirect(args); info != nil {
		s.Logger.Debug().Int("port", info.Port).Str("path", info.Path).Msg("OAuth redirect detected, starting relay")
		opener := s.Opener
		if opener == nil {
			opener = defaultBrowserOpener{}
		}
		s.handleOAuthRelay(conn, reader, info, url, opener)
		return true
	}

	return false
}

// handleHookEvent appends a Claude Code hook event to the in-memory store.
func (s *Server) handleHookEvent(conn net.Conn, args []string) {
	if s.HookEvents != nil {
		evt := ParseHookEventArgs(args)
		s.HookEvents.Append(evt)
		if s.StatsWriter != nil {
			s.StatsWriter.Send(evt)
		}
	}
	resp := Response{Stdout: "ok\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// handleHookSnapshot returns the current per-session hook state as JSON.
func (s *Server) handleHookSnapshot(conn net.Conn) {
	var out string
	if s.HookEvents != nil {
		snap := s.HookEvents.Snapshot()
		data, err := json.Marshal(snap)
		if err != nil {
			s.writeError(conn, err.Error(), 1)
			return
		}
		out = string(data) + "\n"
	} else {
		out = "{}\n"
	}
	resp := Response{Stdout: out}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// handleNetworkEvents returns the current deduplicated ambient network
// activity buffer as JSON. Empty array when the store is unset, matching
// the hook-snapshot convention so a missing daemon feature looks like
// an empty result to the client.
func (s *Server) handleNetworkEvents(conn net.Conn) {
	var out string
	if s.NetworkEvents != nil {
		events := s.NetworkEvents.Snapshot()
		data, err := json.Marshal(events)
		if err != nil {
			s.writeError(conn, err.Error(), 1)
			return
		}
		out = string(data) + "\n"
	} else {
		out = "[]\n"
	}
	resp := Response{Stdout: out}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// handleTrackerDiagnose returns tracker credential status from the daemon's env.
func (s *Server) handleTrackerDiagnose(conn net.Conn, projectDir string) {
	var statuses []tracker.TrackerStatus
	if s.TrackerDiagnoser != nil {
		statuses = s.TrackerDiagnoser(projectDir)
	} else {
		statuses = tracker.DiagnoseTrackers(projectDir, config.UnmarshalSection, os.Getenv)
	}
	data, err := json.Marshal(statuses)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// handleTrackerIssues returns open issues from all configured tracker projects.
func (s *Server) handleTrackerIssues(conn net.Conn) {
	if s.IssueFetcher == nil {
		resp := Response{Stdout: "[]\n"}
		enc := json.NewEncoder(conn)
		_ = enc.Encode(resp)
		return
	}
	results, err := s.IssueFetcher()
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	data, err := json.Marshal(results)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// handleToolStats returns pre-aggregated tool call statistics as JSON.
// The query covers the last 24 hours by default.
func (s *Server) handleToolStats(conn net.Conn) {
	var out string
	if s.StatsStore != nil {
		now := time.Now().UTC()
		since := now.Add(-24 * time.Hour)
		ts, err := s.StatsStore.BuildToolStats(context.Background(), since, now)
		if err != nil {
			s.writeError(conn, err.Error(), 1)
			return
		}
		data, err := json.Marshal(ts)
		if err != nil {
			s.writeError(conn, err.Error(), 1)
			return
		}
		out = string(data) + "\n"
	} else {
		out = "{}\n"
	}
	resp := Response{Stdout: out}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// handleAuditQuery serves "human audit list/show" reads through the daemon,
// which owns the audit DB. An unset store returns an empty array so a missing
// feature looks like an empty result to the client, matching tool-stats.
func (s *Server) handleAuditQuery(conn net.Conn, args []string) {
	if s.AuditStore == nil {
		resp := Response{Stdout: "[]\n"}
		enc := json.NewEncoder(conn)
		_ = enc.Encode(resp)
		return
	}

	f := parseAuditFilter(args)
	events, err := s.AuditStore.Query(context.Background(), f)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	data, err := json.Marshal(events)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// parseAuditFilter builds an audit.Filter from pre-parsed flag args. The args
// arrive as a plain slice (no cobra), so it is self-contained and recognises
// --since/--until (RFC3339), --subject, --tracker, and --limit. Default window
// is the last 7 days up to now.
func parseAuditFilter(args []string) audit.Filter {
	now := time.Now().UTC()
	f := audit.Filter{
		Since: now.Add(-7 * 24 * time.Hour),
		Until: now,
	}

	for i := 0; i < len(args); i++ {
		name, value, consumed := auditFlagValue(args, i)
		if name == "" {
			continue
		}
		applyAuditFlag(&f, name, value)
		i += consumed
	}
	return f
}

// auditFlagValue resolves the flag at args[i] into its name and value,
// supporting both "--flag=value" and "--flag value" forms. consumed is the
// number of extra tokens to skip (1 for the space form). A non-flag or
// unterminated flag yields an empty name.
func auditFlagValue(args []string, i int) (name, value string, consumed int) {
	a := args[i]
	if !strings.HasPrefix(a, "--") {
		return "", "", 0
	}
	if eq := strings.IndexByte(a, '='); eq >= 0 {
		return a[:eq], a[eq+1:], 0
	}
	if i+1 < len(args) {
		return a, args[i+1], 1
	}
	return "", "", 0
}

// applyAuditFlag sets the matching field on f, ignoring unknown flags and
// unparseable time/int values (the defaults already on f then stand).
func applyAuditFlag(f *audit.Filter, name, value string) {
	switch name {
	case "--since":
		if t, err := time.Parse(time.RFC3339, value); err == nil {
			f.Since = t.UTC()
		}
	case "--until":
		if t, err := time.Parse(time.RFC3339, value); err == nil {
			f.Until = t.UTC()
		}
	case "--subject":
		f.Subject = value
	case "--tracker":
		f.TrackerKind = value
	case "--limit":
		if n, err := strconv.Atoi(value); err == nil {
			f.Limit = n
		}
	}
}

// handleAgentStopAsync removes the agent from the list immediately and
// tears down the container in the background. This makes the TUI and
// "human agent list" responsive while the slow container stop happens
// asynchronously.
func (s *Server) handleAgentStopAsync(conn net.Conn, args []string) {
	if len(args) == 0 {
		s.writeError(conn, "agent name required", 1)
		return
	}
	name := args[0]
	if s.AgentCleaner == nil {
		s.writeError(conn, "agent cleanup not available", 1)
		return
	}

	// Remove metadata first so the agent disappears from the list immediately.
	containerID, err := s.AgentCleaner.DecommissionAgent(name)
	if err != nil {
		s.Logger.Warn().Err(err).Str("agent", name).Msg("async agent decommission failed")
	}

	// Notify subscribers (TUI) so they refresh immediately.
	if s.HookEvents != nil {
		s.HookEvents.Append(hookevents.Event{
			EventName: "AgentStopped",
			AgentName: name,
			Timestamp: time.Now().UTC(),
		})
	}

	// Tear down the container in the background.
	if containerID != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if stopErr := s.AgentCleaner.StopContainer(ctx, containerID); stopErr != nil {
				s.Logger.Warn().Err(stopErr).Str("agent", name).Msg("async container stop failed")
			} else {
				s.Logger.Info().Str("agent", name).Msg("async container stop completed")
			}
		}()
	}

	resp := Response{Stdout: fmt.Sprintf("Agent %q stopped\n", name)}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// handleSubscribe keeps the connection open and writes a JSON line each time
// the HookEventStore signals a change. For agent lifecycle events, the event
// carries the agent name so the TUI can remove the instance immediately.
func (s *Server) handleSubscribe(conn net.Conn) {
	if s.HookEvents == nil {
		s.writeError(conn, "hook events not available", 1)
		return
	}
	ch := s.HookEvents.Subscribe()
	defer s.HookEvents.Unsubscribe(ch)

	enc := json.NewEncoder(conn)
	var lastSeq uint64 // monotonic event sequence already delivered
	for {
		// Return on daemon shutdown so this long-lived handler does not pin
		// ListenAndServe's s.wg.Wait() and hang the daemon on SIGINT/SIGTERM.
		select {
		case <-s.shutdown:
			return
		case <-ch:
		}
		// Read the delta by monotonic sequence, not slice length: once the
		// event ring saturates its length stops growing, and a length-based
		// cursor would never advance again — silently dropping notifications.
		newEvents, seq := s.HookEvents.EventsSince(lastSeq)
		lastSeq = seq
		evt := SubscribeEvent{Type: "change"}
		for i := range newEvents {
			if newEvents[i].EventName == "AgentStopped" && newEvents[i].AgentName != "" {
				evt = SubscribeEvent{Type: "agent-stopped", AgentName: newEvents[i].AgentName}
			}
		}
		if err := enc.Encode(evt); err != nil {
			return
		}
	}
}

func (s *Server) writeError(conn net.Conn, msg string, code int) {
	resp := Response{Stderr: msg + "\n", ExitCode: code}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// resolveProjectDir determines the project directory for a request based on the
// client's working directory. Returns "." when no ProjectRegistry is configured.
func (s *Server) resolveProjectDir(cwd string) (string, error) {
	if s.Projects == nil {
		return ".", nil
	}
	if s.Projects.Single() {
		return s.Projects.Entries()[0].Dir, nil
	}
	entry, ok := s.Projects.Resolve(cwd)
	if !ok {
		var dirs []string
		for _, e := range s.Projects.Entries() {
			dirs = append(dirs, e.Dir+" ("+e.Name+")")
		}
		return "", fmt.Errorf("cwd does not match any registered project: %s\nRegistered projects:\n  %s",
			cwd, strings.Join(dirs, "\n  "))
	}
	return entry.Dir, nil
}

// --- destructive operation confirmation ---

// destructiveOp describes a detected destructive command.
type destructiveOp struct {
	Operation string // "DeleteIssue", "EditIssue"
	Tracker   string // tracker kind from args, e.g. "jira"
	Key       string // issue key, e.g. "KAN-1"
}

// detectDestructive inspects CLI args for destructive issue commands.
// Returns the operation details and true if the command is destructive and
// should be intercepted. The daemon always intercepts — --yes is ignored
// when the daemon is running; confirmation must come from the TUI.
func detectDestructive(args []string) (destructiveOp, bool) {
	// Strip flags to find positional subcommands only. A space-separated value
	// flag (e.g. "--tracker jira") must also drop its value token, otherwise
	// that value shifts the positional indices and a delete/edit slips past
	// detection. The known value-flag set is shared with client-side forwarding
	// via internal/cliflags so the two cannot drift apart.
	cleaned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if cliflags.ValueFlags[a] && i+1 < len(args) {
				i++ // skip the flag's value token
			}
			continue
		}
		cleaned = append(cleaned, a)
	}

	// Pattern: <tracker> issue delete <KEY>
	//          <tracker> issue edit <KEY> ...
	if len(cleaned) < 4 {
		return destructiveOp{}, false
	}

	// Find "issue" subcommand. Flags are already stripped above.
	trackerKind := ""
	issueIdx := -1
	for i, a := range cleaned {
		if a == "issue" || a == "issues" {
			issueIdx = i
			break
		}
		trackerKind = a
	}
	if issueIdx < 0 || issueIdx+2 >= len(cleaned) {
		return destructiveOp{}, false
	}

	verb := cleaned[issueIdx+1]
	key := cleaned[issueIdx+2]

	switch verb {
	case "delete":
		return destructiveOp{Operation: "DeleteIssue", Tracker: trackerKind, Key: key}, true
	case "edit":
		return destructiveOp{Operation: "EditIssue", Tracker: trackerKind, Key: key}, true
	case "status":
		// "issue status KEY STATUS" mutates state via TransitionIssue, which the
		// tracker layer already classifies as destructive — gate it too. (Note:
		// the read-only "statuses" listing verb is intentionally not matched.)
		return destructiveOp{Operation: "TransitionIssue", Tracker: trackerKind, Key: key}, true
	case "start":
		// "issue start KEY" transitions to In Progress and assigns the user.
		return destructiveOp{Operation: "StartIssue", Tracker: trackerKind, Key: key}, true
	default:
		return destructiveOp{}, false
	}
}

// handleDestructiveConfirm implements the two-line confirmation protocol for
// destructive operations. It pauses the connection, stores a pending
// confirmation for the TUI, and waits for a decision or timeout.
func (s *Server) handleDestructiveConfirm(conn net.Conn, req Request, op destructiveOp, projectDir string) {
	id := fmt.Sprintf("%s-%s-%d", op.Tracker, op.Key, time.Now().UnixNano())
	prompt := fmt.Sprintf("%s %s?", op.Operation, op.Key)

	pc := &PendingConfirmation{
		ID:        id,
		Operation: op.Operation,
		Tracker:   op.Tracker,
		Key:       op.Key,
		Prompt:    prompt,
		ClientPID: req.ClientPID,
		CreatedAt: time.Now(),
		Decision:  make(chan bool, 1),
	}

	s.PendingConfirms.Add(pc)
	s.Logger.Info().Str("id", id).Str("prompt", prompt).Msg("destructive operation awaiting confirmation")

	// Line 1: tell the client to wait.
	enc := json.NewEncoder(conn)
	resp1 := Response{
		Stderr:        "", // client prints its own "Waiting for confirmation" message
		AwaitConfirm:  true,
		ConfirmID:     id,
		ConfirmPrompt: prompt,
	}
	if err := enc.Encode(resp1); err != nil {
		s.Logger.Warn().Err(err).Msg("failed to write confirm line 1")
		// Remove the entry we just added so it doesn't accumulate in the
		// store with no client to resolve it.
		s.PendingConfirms.ResolveTimeout(id)
		return
	}

	// Wait for TUI decision or timeout.
	var approved bool
	select {
	case approved = <-pc.Decision:
	case <-time.After(confirmTimeout):
		s.Logger.Warn().Str("id", id).Msg("destructive confirmation timed out")
		// Remove from store if still present (Cleanup may have already done it).
		s.PendingConfirms.ResolveTimeout(id)
		approved = false
	}

	if !approved {
		// Denial/timeout: no command ctx exists, so resolve the decision
		// context straight from the request env (falling back to the process).
		s.emitAudit(req.Args, audit.OutcomeDenied, func(k string) string {
			if v, ok := req.Env[k]; ok {
				return v
			}
			return os.Getenv(k)
		})
		resp2 := Response{Stderr: "Operation aborted\n", ExitCode: 1}
		_ = enc.Encode(resp2)
		return
	}

	s.Logger.Info().Str("id", id).Msg("destructive operation approved, executing")

	// Execute the original command. Per-request env values flow via the
	// cobra command context — see executeCommand for the rationale.
	if s.SafeMode {
		if req.Env == nil {
			req.Env = make(map[string]string)
		}
		req.Env["HUMAN_SAFE_MODE"] = "1"
	}
	if projectDir != "." {
		if req.Env == nil {
			req.Env = make(map[string]string)
		}
		req.Env["HUMAN_PROJECT_DIR"] = projectDir
	}

	// Inject --yes so the Cobra command doesn't try to prompt again.
	execArgs := append(req.Args, "--yes")

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := s.CmdFactory()
	cmd.SetArgs(execArgs)
	cmd.SetOut(&stdoutBuf)
	cmd.SetErr(&stderrBuf)
	ctx := env.WithEnv(context.Background(), req.Env)
	ctx = vault.WithResolver(ctx, s.VaultResolver)
	cmd.SetContext(ctx)

	exitCode := 0
	if err := cmd.Execute(); err != nil {
		exitCode = 1
	}

	// Approved-and-executed: record the outcome like the normal path, using the
	// command ctx so the per-request HUMAN_AUDIT_* decision context resolves.
	outcome := audit.OutcomeSuccess
	if exitCode != 0 {
		outcome = audit.OutcomeFailure
	}
	s.emitAudit(req.Args, outcome, func(k string) string { return env.Lookup(ctx, k) })

	resp2 := Response{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
	}
	_ = enc.Encode(resp2)
}

// handlePendingConfirms returns the current pending confirmations as JSON.
func (s *Server) handlePendingConfirms(conn net.Conn) {
	var out string
	if s.PendingConfirms != nil {
		snap := s.PendingConfirms.Snapshot()
		data, err := json.Marshal(snap)
		if err != nil {
			s.writeError(conn, err.Error(), 1)
			return
		}
		out = string(data) + "\n"
	} else {
		out = "[]\n"
	}
	resp := Response{Stdout: out}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// handleConfirmOp resolves a pending confirmation with the given decision.
// Expected args: [ID, "yes"|"no"]. approverPID prevents self-approval.
func (s *Server) handleConfirmOp(conn net.Conn, args []string, approverPID int) {
	if len(args) < 2 {
		s.writeError(conn, "usage: confirm-op ID yes|no", 1)
		return
	}
	id := args[0]
	approved := args[1] == "yes"

	if s.PendingConfirms == nil {
		s.writeError(conn, "confirmation store not available", 1)
		return
	}
	if err := s.PendingConfirms.Resolve(id, approved, approverPID); err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}

	resp := Response{Stdout: "ok\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}
