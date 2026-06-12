// Package gui serves the browser dashboard: a small HTTP + WebSocket API
// over the daemon's in-memory state plus the embedded React frontend.
//
// It lives outside internal/daemon because snapshot assembly reuses
// internal/claude/monitor, which itself imports internal/daemon — placing
// the GUI inside the daemon package would create an import cycle.
package gui

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
)

// Interfaces over daemon state so handlers stay testable without a
// running daemon ('Extract Interface' + 'Inject Dependencies').
type (
	// SnapshotProvider returns the current dashboard snapshot.
	SnapshotProvider interface {
		Snapshot(ctx context.Context) (SnapshotDTO, error)
	}

	// IssueFetcher fetches open issues from all configured trackers.
	IssueFetcher func() ([]daemon.TrackerIssuesResult, error)

	// ProjectLister returns the projects registered with the daemon.
	ProjectLister func() []daemon.ProjectInfo

	// ConfirmStore exposes pending destructive-operation confirmations.
	// *daemon.PendingConfirmStore satisfies it.
	ConfirmStore interface {
		Snapshot() []daemon.PendingConfirm
		Resolve(id string, approved bool, approverPID int) error
	}

	// LogModeStore reads and writes the proxy traffic log mode.
	LogModeStore interface {
		Get() string
		Set(mode string) error
	}

	// CommandRunner executes a CLI command line and returns its stdout.
	// Production routes through the daemon loopback so destructive-op
	// interception and per-project env scoping stay on one code path.
	CommandRunner interface {
		RunCapture(args []string) ([]byte, error)
	}

	// AgentRunner dispatches and stops daemon-managed headless agents.
	AgentRunner interface {
		Dispatch(ctx context.Context, opts DispatchOpts) (name string, err error)
		Stop(ctx context.Context, name string) error
	}
)

// DispatchOpts describes a headless agent dispatch request.
type DispatchOpts struct {
	Prompt     string // skill invocation, e.g. "/human-execute HUM-42"
	ProjectDir string // workspace the agent operates on
}

// Server is the GUI HTTP + WebSocket listener.
type Server struct {
	Addr   string
	Token  string
	Logger zerolog.Logger

	Snapshots SnapshotProvider
	Issues    IssueFetcher
	Projects  ProjectLister
	Confirms  ConfirmStore
	LogMode   LogModeStore
	Commands  CommandRunner
	Agents    AgentRunner
	Hooks     *daemon.HookEventStore

	// ApproverPID identifies this server when resolving confirmations.
	// The daemon passes its own PID; it can never equal a requesting
	// Claude instance's PID, so the store's sanity guard stays intact.
	ApproverPID int

	hub *Hub
}

// AttachHub wires the WebSocket hub. Must be called before ListenAndServe
// (or Handler in tests) for push delivery to work.
func (s *Server) AttachHub(hub *Hub) { s.hub = hub }

// Handler builds the full route table. Exposed for httptest-based tests.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth", s.handleAuth)
	s.registerAPIRoutes(mux)
	mux.Handle("/", staticHandler())
	return s.guardOrigin(mux)
}

// registerAPIRoutes mounts all authenticated endpoints.
func (s *Server) registerAPIRoutes(mux *http.ServeMux) {
	auth := s.requireAuth
	mux.Handle("GET /api/snapshot", auth(http.HandlerFunc(s.handleSnapshot)))
	mux.Handle("GET /api/projects", auth(http.HandlerFunc(s.handleProjects)))
	mux.Handle("GET /api/issues", auth(http.HandlerFunc(s.handleIssues)))
	mux.Handle("GET /api/log-mode", auth(http.HandlerFunc(s.handleLogModeGet)))
	mux.Handle("PUT /api/log-mode", auth(http.HandlerFunc(s.handleLogModeSet)))
	mux.Handle("GET /api/confirms", auth(http.HandlerFunc(s.handleConfirmsGet)))
	mux.Handle("POST /api/confirms/{id}", auth(http.HandlerFunc(s.handleConfirmResolve)))
	mux.Handle("POST /api/tickets", auth(http.HandlerFunc(s.handleTicketCreate)))
	mux.Handle("POST /api/agents", auth(http.HandlerFunc(s.handleAgentDispatch)))
	mux.Handle("POST /api/agents/{name}/stop", auth(http.HandlerFunc(s.handleAgentStop)))
	mux.Handle("GET /ws", auth(http.HandlerFunc(s.handleWS)))
}

// ListenAndServe starts the HTTP listener and blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", s.Addr)
	if err != nil {
		return errors.WrapWithDetails(err, "gui listen failed", "addr", s.Addr)
	}

	if !isLoopbackHostport(s.Addr) {
		// The auth cookie travels over plain HTTP; off-host exposure would
		// let anyone on the network steal the daemon token.
		s.Logger.Warn().Str("addr", s.Addr).Msg("GUI bound to a non-loopback address — the token cookie is sent over plain HTTP")
	}

	httpSrv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	if s.hub != nil {
		go s.runHookBridge(s.hub, ctx.Done())
	}

	s.Logger.Info().Str("addr", ln.Addr().String()).Msg("gui listening")

	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return errors.WrapWithDetails(err, "gui serve failed", "addr", s.Addr)
	}
	return nil
}
