package monarch

import (
	"context"
	"embed"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude"
)

// webAssets holds the read-only dashboard (HTML/CSS/JS) served by the web
// console. Embedding keeps monarch a single self-contained binary with no
// runtime asset path to configure.
//
//go:embed all:web
var webAssets embed.FS

const (
	// webWorkBoardSince, webBurnDayStart, and webPresenceWindow mirror the exact
	// time windows the former TUI used, so the web console is at parity: the work
	// board shows the last 24h, burn is since the start of the UTC day, and a
	// daemon counts as connected only if seen within a few heartbeat intervals.
	webWorkBoardSince = 24 * time.Hour
	webPresenceWindow = 3 * DefaultHeartbeatInterval
	// webStoreReadLimit bounds each snapshot's store reads so a slow query can
	// never stall an HTTP response indefinitely.
	webStoreReadLimit = 3 * time.Second
	// webShutdownGrace bounds graceful drain on context cancel.
	webShutdownGrace = 5 * time.Second
)

// WebServer serves the read-only swarm dashboard: an embedded single-page UI
// plus a JSON snapshot endpoint it polls. It reads the same Store the ingest
// server writes (SQLite caps to one connection, so concurrent reads are safe)
// and exposes only identity-free work data — never a person.
type WebServer struct {
	Addr   string
	Store  *Store
	Logger zerolog.Logger
}

// capView is the capacity headcount with stable lowercase JSON keys, decoupling
// the wire shape from the internal Capacity domain type.
type capView struct {
	Daemons int `json:"daemons"`
	Busy    int `json:"busy"`
	Blocked int `json:"blocked"`
	Idle    int `json:"idle"`
}

// workView is one work-board row. An absent ticket is rendered as an em dash so
// the wire payload matches the old TUI's presentation verbatim.
type workView struct {
	Daemon string `json:"daemon"`
	Ticket string `json:"ticket"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	State  string `json:"state"`
}

// burnView is one burn row with a pre-formatted token Display (e.g. "1.5K", or
// an em dash for zero) so the browser never re-implements token formatting.
type burnView struct {
	Key     string `json:"key"`
	Tokens  int    `json:"tokens"`
	Display string `json:"display"`
}

// Snapshot is the full console state in one payload. A single endpoint keeps the
// three panes mutually consistent and the browser polling trivial.
type Snapshot struct {
	GeneratedAt  time.Time  `json:"generated_at"`
	Capacity     capView    `json:"capacity"`
	Board        []workView `json:"board"`
	BurnByTicket []burnView `json:"burn_by_ticket"`
	BurnByRepo   []burnView `json:"burn_by_repo"`
}

// emDash marks an absent value, mirroring the TUI's convention so the web view
// reads identically.
const emDash = "—"

// ListenAndServe serves the dashboard until ctx is cancelled, then drains
// gracefully. Like the ingest server it binds its own listener so the bound
// address (including an ephemeral :0 port in tests) is observable.
func (w *WebServer) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Handler:           w.handler(),
		ReadHeaderTimeout: webStoreReadLimit,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), webShutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	w.Logger.Info().Str("addr", w.Addr).Msg("monarch web console listening")
	srv.Addr = w.Addr
	// http.ErrServerClosed is the expected outcome of a graceful Shutdown, not a
	// failure, so it is swallowed.
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return errors.WrapWithDetails(err, "monarch web serve failed", "addr", w.Addr)
	}
	return nil
}

// handler builds the Echo router: the JSON snapshot API plus the embedded
// static UI. Split out so tests can exercise routes without binding a port.
func (w *WebServer) handler() *echo.Echo {
	e := echo.New()
	e.GET("/api/snapshot", w.handleSnapshot)
	e.StaticFS("/", echo.MustSubFS(webAssets, "web"))
	return e
}

// handleSnapshot reads the current swarm state and returns it as JSON. Store
// read errors collapse to empty panes (see snapshot) so a transient hiccup
// never turns into an HTTP error the browser has to special-case.
func (w *WebServer) handleSnapshot(c *echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), webStoreReadLimit)
	defer cancel()
	return c.JSON(http.StatusOK, w.snapshot(ctx))
}

// snapshot runs the four store reads over the mirrored TUI windows. Read errors
// are intentionally swallowed to empty slices: the console degrades to blank
// panes rather than failing, exactly as the TUI did.
func (w *WebServer) snapshot(ctx context.Context) Snapshot {
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	board, _ := w.Store.WorkBoard(ctx, now.Add(-webWorkBoardSince))
	burnTicket, _ := w.Store.BurnByTicket(ctx, dayStart)
	burnRepo, _ := w.Store.BurnByRepo(ctx, dayStart)
	capacity, _ := w.Store.Capacity(ctx, now.Add(-webPresenceWindow))
	return buildSnapshot(now, board, burnTicket, burnRepo, capacity)
}

// buildSnapshot maps domain rows to the wire shape. It is pure (no store, no
// clock beyond the passed-in now) so the presentation rules — ticket fallback,
// token formatting, zero-as-dash — are unit-testable in isolation.
func buildSnapshot(now time.Time, board []WorkItem, burnTicket, burnRepo []BurnRow, capacity Capacity) Snapshot {
	return Snapshot{
		GeneratedAt:  now,
		Capacity:     capView(capacity),
		Board:        toWorkViews(board),
		BurnByTicket: toBurnViews(burnTicket),
		BurnByRepo:   toBurnViews(burnRepo),
	}
}

func toWorkViews(board []WorkItem) []workView {
	views := make([]workView, 0, len(board))
	for _, w := range board {
		ticket := w.TicketKey
		if ticket == "" {
			ticket = emDash
		}
		views = append(views, workView{
			Daemon: w.DaemonID,
			Ticket: ticket,
			Repo:   w.Repo,
			Branch: w.Branch,
			State:  w.State,
		})
	}
	return views
}

func toBurnViews(rows []BurnRow) []burnView {
	views := make([]burnView, 0, len(rows))
	for _, r := range rows {
		total := r.InputTokens + r.OutputTokens + r.CacheCreate + r.CacheRead
		display := emDash
		if total > 0 {
			display = claude.FormatTokens(total)
		}
		key := r.Key
		if key == "" {
			key = emDash
		}
		views = append(views, burnView{Key: key, Tokens: total, Display: display})
	}
	return views
}
