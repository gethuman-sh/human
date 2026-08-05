//go:build wailsapp

// Package main implements the workflow-board desktop app (Wails v2).
//
// The whole file set is guarded by the `wailsapp` build tag because Wails is a
// cgo backend (webkit2gtk / WebView2 / Obj-C) that cannot compile on a plain
// toolchain without the native webview headers. The tag keeps `go vet ./...`,
// `go list ./...` and the existing CI Linux build green — the desktop binary is
// produced only via `wails build` on the 3-runner matrix (see Makefile +
// .github/workflows/desktop.yml).
//
// The tag is deliberately NOT named `desktop`: Wails reserves `desktop` as its
// own output-mode tag and strips it before the host-side binding-generation
// build, which would hide every file here and break `wails build`. A neutral
// tag survives both the binding pass and the final compile, while Wails still
// adds `desktop` itself for the cgo backend selection.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/appearance"
	"github.com/gethuman-sh/human/internal/appsession"
	"github.com/gethuman-sh/human/internal/board"
	"github.com/gethuman-sh/human/internal/boardprefs"
	"github.com/gethuman-sh/human/internal/costledger"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/ideaspace"
	"github.com/gethuman-sh/human/internal/pipeline"
	"github.com/gethuman-sh/human/internal/recentprojects"
	"github.com/gethuman-sh/human/internal/vieweridentity"
)

// App is the Go backend bound into the webview via options.App.Bind. Every
// method here is callable from the TypeScript frontend. The app talks ONLY to
// the daemon client (daemon.GetTrackerIssues / daemon.BoardTransition /
// daemon.Subscribe) — never directly to a tracker or forge — so all credential
// handling, role resolution and the destructive-confirm bypass stay in the
// daemon.
//
// The one exception is Instances() (instances.go), which discovers running
// Claude Code processes in-process via the monitor package. That path needs no
// credentials and the monitor runs alongside the daemon calls, so
// it is consistent with the credential-only rationale above — and it cannot be
// a daemon route regardless, since monitor imports daemon (an import cycle).
type App struct {
	ctx context.Context
	// ideas holds the idea-space placement (ticket → sub-column). Local file
	// I/O rather than a daemon route: this is UI preference state that must
	// never touch the ticket, in line with the credential-only rationale above.
	ideas *ideaspace.Store
	// recents holds the Projects Overview's most-recently-opened list. Same
	// local-file rationale as ideas: which projects were opened, and in what
	// order, is desktop-workspace state, never tracker or daemon state.
	recents *recentprojects.Store
	// prefs holds the board view preferences (per-column card order, hidden
	// tickets) — the same local-only rationale as ideas.
	prefs *boardprefs.Store
	// cache holds the last-known full board snapshot, keyed by project, so a
	// cold open paints instantly from it before the live fetch lands
	// (stale-while-revalidate). Local UI acceleration only — never tracker state.
	// session tracks which daemon process (by PID) this app currently manages,
	// so a future launch can tell a crash-orphaned daemon apart from one a user
	// intentionally left running standalone (SC-3015). Local-only, same
	// rationale as ideas/recents/prefs above.
	session *appsession.Store
	// closeInFlight guards against a second window-close click stacking a
	// second busy-check/dialog while one is already deciding (SC-3015).
	closeInFlight atomic.Bool
	// readyToQuit tells the re-entrant OnBeforeClose invocation (Wails' own
	// runtime.Quit calls it a second time) to permit the close this time,
	// instead of preventing it again — see closeflow.go.
	readyToQuit atomic.Bool
	// currentUser caches the tracker's answer for who is authenticated, the
	// FALLBACK identity used only when .humanconfig declares no "me" names.
	// Reused for the session (identity does not change while the app runs).
	// Only a *successful* fetch is memoized (currentUserResolved) — a transient
	// failure (locked vault prompt, credential blip, daemon still on an older
	// protocol) must not latch the board into "no dimming" for the rest of the
	// process lifetime, since the desktop app is a long-lived tray process; the
	// next refresh retries until it succeeds.
	currentUserMu       sync.Mutex
	currentUser         string
	currentUserResolved bool
	// currentUserFetch is the actual IPC call, indirected so viewerIdentity's
	// memoize-only-on-success retry logic can be exercised in a test without a
	// running daemon. Always daemon.GetCurrentUserName outside tests.
	currentUserFetch func(addr, token string) (string, error)
	// viewerConfig reads the declared "me" identity for a project directory,
	// indirected for the same testing reason. Always vieweridentity.Load.
	viewerConfig func(dir string) (vieweridentity.Identity, error)
	// appearanceConfig reads the declared "ui" appearance for a project
	// directory, indirected for the same testing reason as viewerConfig.
	// Always appearance.Load.
	appearanceConfig func(dir string) (appearance.Appearance, error)
}

// NewApp constructs the backend. Wails injects the lifecycle context via
// startup, so there is nothing to wire here.
func NewApp() *App {
	return &App{
		ideas:            ideaspace.NewStore(ideaspace.DefaultPath()),
		recents:          recentprojects.NewStore(recentprojects.DefaultPath()),
		prefs:            boardprefs.NewStore(boardprefs.DefaultPath()),
		session:          appsession.NewStore(appsession.DefaultPath()),
		currentUserFetch: daemon.GetCurrentUserName,
		viewerConfig:     vieweridentity.Load,
		appearanceConfig: appearance.Load,
	}
}

// Card and BoardData are the frontend-facing board shapes. They are ALIASES of
// the daemon's wire types (moved there so the daemon can compose and return the
// board; internal/board already imports internal/daemon, so the types could not
// live alongside Compose without cycling). Aliases rather than new types so every
// existing reference and the frontend's JSON shape stay byte-identical.
type Card = daemon.BoardViewCard

type BoardData = daemon.BoardView

// Cards fetches the current board state from the daemon and flattens the single
// PM-role result into a card list, dropping hidden cards. v1 is single
// project/tracker, so we take the first PM-role result. Any per-result fetch
// error is surfaced to the frontend rather than dropped, so the user sees a
// banner instead of an empty board.
func (a *App) Cards() (BoardData, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return BoardData{}, err
	}

	view, results, err := a.boardView(info)
	if err != nil {
		return BoardData{}, daemonCause(err)
	}
	project := projectKeyOf(info)
	data := applyLocal(view, a.ideas.Assignments(project), cardMockups(), a.prefs.Snapshot(project), a.viewerIdentity(info), a.boardAppearance(info))
	// The keep sets come from `results` — the same fetch CanPrune judges — not
	// from the composed view, which is a separate request that can answer with
	// an empty board while this one is healthy (SC-2400).
	board.PrunePrefs(results, project,
		board.PruneTarget{Store: a.prefs, Keep: board.PrefsKeep(results)},
		board.PruneTarget{Store: a.ideas, Keep: board.IdeaKeep(results)},
	)
	return data, nil
}

// daemonCause rewrites a daemon-client error for the Wails boundary: only
// err.Error() crosses to the frontend, and for daemon failures that is the
// generic "daemon command failed" wrapper. Folding in the cause chain and the
// daemon's stderr detail makes every board banner name what actually broke —
// an error surface must carry actionable information or not appear at all.
func daemonCause(err error) error {
	if err == nil {
		return nil
	}
	msg := errors.CauseChain(err)
	if stderr, ok := errors.AllDetails(err)["stderr"].(string); ok && strings.TrimSpace(stderr) != "" {
		msg += ": " + strings.TrimSpace(stderr)
	}
	return fmt.Errorf("%s", msg)
}

// IssueDetail is the full-ticket payload for the board's detail panel — only
// the fields the panel renders beyond what the card already carries.
// DescriptionHTML is rendered and sanitized by the daemon; the frontend may
// inject it verbatim.
type IssueDetail struct {
	Title           string `json:"title"`
	Assignee        string `json:"assignee,omitempty"`
	Description     string `json:"description,omitempty"`
	DescriptionHTML string `json:"descriptionHTML,omitempty"`
	// Comment-sourced sections the panel shows below the description, each
	// daemon-rendered to sanitized HTML so the frontend injects them verbatim.
	ReviewFindingsHTML string `json:"reviewFindingsHTML,omitempty"`
	FailureReasonHTML  string `json:"failureReasonHTML,omitempty"`
	FixSummaryHTML     string `json:"fixSummaryHTML,omitempty"`
}

// GetIssueDetail fetches one full ticket from the daemon. The detail panel
// calls it on open because list fetches on some trackers (e.g. Shortcut)
// return slim payloads without descriptions, so the card's own description
// can be empty even for a ticket that has one.
func (a *App) GetIssueDetail(trackerKind, trackerName, key string) (IssueDetail, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IssueDetail{}, err
	}
	issue, err := daemon.GetTrackerIssue(info.Addr, info.Token, trackerKind, trackerName, key)
	if err != nil {
		return IssueDetail{}, daemonCause(err)
	}
	return IssueDetail{
		Title:              issue.Title,
		Assignee:           issue.Assignee,
		Description:        issue.Description,
		DescriptionHTML:    issue.DescriptionHTML,
		ReviewFindingsHTML: issue.ReviewFindingsHTML,
		FailureReasonHTML:  issue.FailureReasonHTML,
		FixSummaryHTML:     issue.FixSummaryHTML,
	}, nil
}

// TicketCost fetches the durable per-ticket cost/time rollup for the detail
// panel. Reached from the frontend through the hand-written AppBindings
// interface (this repo uses no generated Wails bindings), so the Go signature
// here and the TS declaration in board.ts must agree on shape by hand.
func (a *App) TicketCost(key string) (costledger.TicketCost, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return costledger.TicketCost{}, err
	}
	r, err := daemon.GetTicketCost(info.Addr, info.Token, key)
	if err != nil {
		return costledger.TicketCost{}, daemonCause(err)
	}
	return r, nil
}

// SetIdeaColumn persists the idea-space placement for one ticket. Purely
// local UI state — never a tracker write or a board transition.
func (a *App) SetIdeaColumn(pmKey string, col int) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return a.ideas.Set(projectKeyOf(info), pmKey, col)
}

// SetColumnOrder persists the hand-sorted card order for one queue column.
// Purely local UI state — never a tracker write or a board transition.
func (a *App) SetColumnOrder(queue string, keys []string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return a.prefs.SetOrder(projectKeyOf(info), queue, keys)
}

// SetCardHidden parks a ticket off the board (or restores it). Purely local
// UI state — the ticket on the tracker is untouched.
func (a *App) SetCardHidden(pmKey string, hidden bool) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return a.prefs.SetHidden(projectKeyOf(info), pmKey, hidden)
}

// CardsQuick fetches issue titles only — skipping the per-ticket comment scan
// that derives board stages — and places every open PM issue in the Backlog. It
// returns far faster than Cards(), so the board can render titles immediately;
// the subsequent Cards() call reconciles each card into its real stage. Docker
// availability is assumed here (the real value arrives with Cards()) so the quick
// path never blocks on a Docker round-trip.
func (a *App) CardsQuick() (BoardData, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return BoardData{}, err
	}

	results, err := daemon.GetTrackerIssuesLite(info.Addr, info.Token)
	if err != nil {
		return BoardData{}, daemonCause(err)
	}
	project := projectKeyOf(info)
	return boardFromResults(results, true, a.ideas.Assignments(project), cardMockups(), a.prefs.Snapshot(project), a.viewerIdentity(info), a.boardAppearance(info)), nil
}

// viewerIdentity returns the names that mean "me" for the board's ownership
// dimming. The declared identity in .humanconfig wins: it is the only source
// that covers every tracker (a GitHub login and a Shortcut display name are the
// same person), needs no credential or live call, and cannot fail in a way that
// silently makes every ticket look like yours.
//
// Only when nothing is declared does it fall back to asking the PM tracker who
// is authenticated — one provider's opinion, so it can name you on Shortcut and
// nowhere else, but better than no distinction at all for an unconfigured
// install. That fallback degrades to an empty identity on any failure (older
// daemon, no PM identity, credential blip): the board renders normally, just
// without the mine/not-mine distinction, and because only success is memoized
// the next call retries instead of latching the failure in for the app's life.
func (a *App) viewerIdentity(info daemon.DaemonInfo) vieweridentity.Identity {
	if dir := projectKeyOf(info); dir != "" {
		if declared, err := a.viewerConfig(dir); err == nil && declared.Known() {
			return declared
		}
	}

	a.currentUserMu.Lock()
	defer a.currentUserMu.Unlock()

	if a.currentUserResolved {
		return identityOf(a.currentUser)
	}
	name, err := a.currentUserFetch(info.Addr, info.Token)
	if err != nil {
		return vieweridentity.Identity{}
	}
	a.currentUser = name
	a.currentUserResolved = true
	return identityOf(a.currentUser)
}

// boardAppearance resolves how faint a not-mine card should render for this
// project, as a percent of full opacity. It is deliberately NOT memoized like
// currentUser: the value is a person's live preference, so editing it in the
// settings page and refreshing the board must show the change without
// restarting the app.
//
// Zero — no project dir, an unreadable or unparseable config, nothing
// declared, or a value outside the usable range — means "say nothing", and
// the frontend then leaves the stylesheet's shipped default in force.
func (a *App) boardAppearance(info daemon.DaemonInfo) int {
	dir := projectKeyOf(info)
	if dir == "" {
		return 0
	}
	ui, err := a.appearanceConfig(dir)
	if err != nil {
		return 0
	}
	return ui.DimPercent()
}

// identityOf lifts a single tracker-reported name into an Identity, dropping an
// empty one so "the tracker knows of no name" stays "viewer unknown".
func identityOf(name string) vieweridentity.Identity {
	if strings.TrimSpace(name) == "" {
		return vieweridentity.Identity{}
	}
	return vieweridentity.Identity{Names: []string{name}}
}

// projectKeyOf identifies the project a board snapshot belongs to. v1 serves one
// project, so the first registered project's directory is the key; empty when a
// daemon predates the Projects field, which degrades to a single global cache.
func projectKeyOf(info daemon.DaemonInfo) string {
	if len(info.Projects) > 0 {
		return info.Projects[0].Dir
	}
	return ""
}

// boardView gets the composed board from the daemon, which is the machine that
// should own it. It also returns the raw results, because pref pruning still
// needs to see every tracker's outcome — the composed view is PM-only by design.
//
// The fallback covers a daemon older than the board-view route: compose locally
// from the same raw results. Same function, same output — only the machine doing
// the work differs, so a version-skewed pair still renders a correct board
// rather than nothing.
func (a *App) boardView(info daemon.DaemonInfo) (daemon.BoardView, []daemon.TrackerIssuesResult, error) {
	results, err := daemon.GetTrackerIssues(info.Addr, info.Token)
	if err != nil {
		return daemon.BoardView{}, nil, err
	}
	if view, vErr := daemon.GetBoardView(info.Addr, info.Token); vErr == nil {
		return view, results, nil
	}
	return board.Compose(results, dockerAvailable()), results, nil
}

// boardFromResults composes the shared board and then applies this viewer's own
// overlay. The split is the point: Compose produces what is true of the project,
// applyLocal adds what is true only of the person looking.
func boardFromResults(results []daemon.TrackerIssuesResult, dockerAvailable bool, ideaCols map[string]int, mocks map[string]cardMockupInfo, prefs boardprefs.Prefs, viewer vieweridentity.Identity, dimPercent int) BoardData {
	return applyLocal(board.Compose(results, dockerAvailable), ideaCols, mocks, prefs, viewer, dimPercent)
}

// applyLocal fills the fields Compose deliberately leaves blank because they
// belong to the viewer, not the project: the idea-space sub-column, the locally
// generated mockup links, the hand-sorted column order, and the hide flag.
//
// Hidden cards are marked, not dropped — the frontend filters them so a user can
// reveal them without a refetch, which is why Compose returns them at all.
func applyLocal(view daemon.BoardView, ideaCols map[string]int, mocks map[string]cardMockupInfo, prefs boardprefs.Prefs, viewer vieweridentity.Identity, dimPercent int) BoardData {
	view.ColumnOrder = prefs.Columns
	for i := range view.Cards {
		c := &view.Cards[i]
		if c.Stage == string(daemon.BoardIdeas) {
			// Missing key → zero value → leftmost column, the loose default.
			c.IdeaColumn = ideaCols[c.Key]
		}
		mock := mocks[c.Key]
		c.MockupSlug, c.MockupState = mock.Slug, mock.State
		c.MockupChosenSlug, c.MockupChosenFile = mock.ChosenSlug, mock.ChosenFile
		_, c.Hidden = prefs.Hidden[c.Key]
	}
	// Ownership is viewer-local, like Hidden: dim cards owned by someone else.
	board.MarkOwnership(view.Cards, viewer)
	// The dimming STRENGTH is viewer-local for the same reason the identity is:
	// both are declared in this machine's .humanconfig, and neither belongs to
	// the shared board Compose returns.
	view.DimPercent = dimPercent
	return view
}

// formatStageTime renders a marker time for the frontend, empty when the card
// has no derived stage timestamp (e.g. the quick-fetch path).
func formatStageTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// DaemonStatus reports whether the human daemon is currently reachable. The
// frontend polls this independently of Cards() because Cards() returns an
// error the instant the daemon is unreachable and stops there — the one case
// this indicator exists to show would otherwise never populate a "reachable"
// field. Combines IsReachable() (authoritative TCP dial, works across process
// namespaces e.g. host <-> devcontainer) with ReadAlivePid() (same-host
// PID-file liveness) so a daemon that is alive but momentarily not yet
// listening still reads as reachable — a dual-source check.
func (a *App) DaemonStatus() bool {
	info, err := daemon.ReadInfo()
	if err == nil && info.IsReachable() {
		// Best-effort: keep the app-session marker fresh (internal/appsession)
		// so a crash between polls is still detectable as an orphan at the
		// next launch, and a daemon handover (new PID, same logical daemon)
		// is re-marked within one poll interval (SC-3015).
		_ = a.session.Mark(os.Getpid(), info.PID)
		return true
	}
	_, alive := daemon.ReadAlivePid()
	return alive
}

// DaemonBusy reports whether stopping the daemon right now would end
// in-flight agent work: either a live Claude Code instance (host or
// container) with status "working" — the same discovery Instances() already
// runs in-process — or the daemon reporting at least one project stage still
// holding a live (non-expired) lease. The close flow (closeflow.go) uses this
// to choose between a silent stop and the three-way confirmation dialog
// (SC-3015).
func (a *App) DaemonBusy() (bool, error) {
	info, err := daemon.ReadInfo()
	if err != nil || !info.IsReachable() {
		// Nothing reachable to protect, and StopIfRunning no-ops on an
		// already-dead daemon — treat as idle rather than erroring the close.
		return false, nil
	}
	if instances, instErr := a.Instances(); instErr == nil {
		for _, ag := range instances.Agents {
			if ag.Status == "working" {
				return true, nil
			}
		}
	}
	status, err := daemon.GetDaemonBusy(info.Addr, info.Token)
	if err != nil {
		// A daemon predating this route, or a transient RPC hiccup, must never
		// block every close forever: fall back to "not busy by this signal" —
		// the instance check above already ran and stands on its own.
		return false, nil
	}
	return status.Busy, nil
}

// Doctor returns the daemon's substrate health checks for the rail LED. An
// unreachable daemon (or a daemon predating the doctor route) surfaces as an
// unhealthy result with a single explanatory check rather than an error, so
// the LED always has something truthful to show.
func (a *App) Doctor() daemon.DoctorData {
	info, err := daemon.ReadInfo()
	if err != nil || !info.IsReachable() {
		return daemon.DoctorData{Checks: []daemon.DoctorCheck{
			{ID: "daemon", Name: "daemon", OK: false, Detail: "not reachable — start it with 'human daemon'"},
		}}
	}
	data, err := daemon.GetDoctor(info.Addr, info.Token, false)
	if err != nil {
		return daemon.DoctorData{Checks: []daemon.DoctorCheck{
			{ID: "daemon", Name: "daemon", OK: false, Detail: "doctor unavailable: " + err.Error()},
		}}
	}
	return data
}

// Transition advances a card one stage by delegating to the daemon's
// board-transition route. The daemon is authoritative: it re-derives the card
// from live comments and enforces forward-only/gated rules, so an out-of-date
// optimistic move in the UI is corrected on the next Cards() reconcile.
func (a *App) Transition(pmKey, pmTitle, from, to string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.BoardTransition(info.Addr, info.Token, daemon.BoardTransitionRequest{
		PMKey:   pmKey,
		PMTitle: pmTitle,
		From:    daemon.BoardStage(from),
		To:      daemon.BoardStage(to),
	}))
}

// Reopen restarts a stage the pipeline resolved — a nothing-to-do or
// no-fix-needed terminal the reader judges wrong. It is deliberately a separate
// entry point from Transition: the daemon's retry machinery drives the same
// same-stage request, and only an explicitly human call may re-run a clean
// terminal. Without it a resolved card had no gesture at all and could be
// recovered only by editing the tracker by hand.
func (a *App) Reopen(pmKey, pmTitle, stage string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.BoardTransition(info.Addr, info.Token, daemon.BoardTransitionRequest{
		PMKey:   pmKey,
		PMTitle: pmTitle,
		From:    daemon.BoardStage(stage),
		To:      daemon.BoardStage(stage),
		Reopen:  true,
	}))
}

// FixBug asks the daemon to launch the autonomous bug-fix pipeline
// (/human-autofix) on a bug ticket — the Bugs pane's Fix drop. Like Transition
// it goes through the daemon so the agent runs containerized with the daemon's
// credentials; the daemon guards against double-launches, so an optimistic
// re-drop is safe.
func (a *App) FixBug(pmKey, pmTitle string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.BoardFix(info.Addr, info.Token, daemon.BoardFixRequest{
		PMKey:   pmKey,
		PMTitle: pmTitle,
	}))
}

// FixSecurity asks the daemon to launch the security-fix pipeline
// (/human-security-fix) on a security ticket — the Security section's Fix drop.
// Like FixBug it goes through the daemon so the agent runs containerized with
// the daemon's credentials; the daemon guards against double-launches, so an
// optimistic re-drop is safe.
func (a *App) FixSecurity(pmKey, pmTitle string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.BoardSecurityFix(info.Addr, info.Token, daemon.SecurityFixRequest{
		PMKey:   pmKey,
		PMTitle: pmTitle,
	}))
}

// FindRelatedWork asks the daemon to launch the filing-time related-work triage
// (/human-relate) on one bug — the Bugs pane's on-demand card action for a bug
// that carries no completed record yet (a sweep-filed bug, or one whose run died
// halfway). Like FixBug it goes through the daemon so the agent runs
// containerized with the daemon's credentials; the daemon tears down any prior
// relate agent for the key, so a re-click is safe (SC-2405).
func (a *App) FindRelatedWork(pmKey, pmTitle string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.Relate(info.Addr, info.Token, daemon.RelateRequest{
		PMKey:   pmKey,
		PMTitle: pmTitle,
	}))
}

// ChooseOption records the user's pick from a card's open decision block and
// relaunches the block's stage with the choice — the click on a choice the
// reviewer offered is the consent, exactly like a drag is for a transition.
func (a *App) ChooseOption(pmKey, optionID string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.SendBoardOption(info.Addr, info.Token, daemon.BoardOptionRequest{
		PMKey:    pmKey,
		OptionID: optionID,
	}))
}

// GenerateFeatures asks the daemon to launch the human-features skill, which
// regenerates FEATURE.json. Like Transition it goes through the daemon so it
// runs the skill in the project's devcontainer — the same containerized agent
// path a kanban stage transition uses. It returns once the agent is launched,
// not when generation finishes; the pane polls Features() for the new file.
func (a *App) GenerateFeatures() error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.GenerateFeatures(info.Addr, info.Token))
}

// FindBugs asks the daemon to launch the human-findbugs sweep for the registered
// project — the Bugs pane's Findbugs button. Like GenerateFeatures it goes
// through the daemon so the sweep runs containerized with the daemon's
// credentials, and it returns once the agent is launched; surviving findings
// surface as bug cards on the next Cards() reconcile.
func (a *App) FindBugs() error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.StartFindbugs(info.Addr, info.Token))
}

// FindbugsHunting reports whether a findbugs sweep is currently running for any
// registered project. It reads the sweep's own pipeline state file directly (the
// same project-local read pattern MockupSets uses), so the pane can show a live
// hunt indicator without a dedicated daemon route. A sweep sets status
// running/triaging for its whole run and cleans the file up at the end; a stale
// status older than findbugsHuntWindow (a crashed sweep) is treated as finished.
func (a *App) FindbugsHunting() bool {
	return sweepRunning("bugs")
}

// FindSecurity asks the daemon to launch the human-security sweep for the
// registered project — the Security pane's Find Security button, the exact
// counterpart to FindBugs. It runs containerized with the daemon's credentials
// and returns once the agent is launched; surviving findings surface as security
// cards on the next Cards() reconcile.
func (a *App) FindSecurity() error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.StartFindsecurity(info.Addr, info.Token))
}

// SecurityHunting reports whether a human-security sweep is currently running —
// the Security counterpart to FindbugsHunting, reading the "security" pipeline
// state the sweep maintains under .human/security/.
func (a *App) SecurityHunting() bool {
	return sweepRunning("security")
}

// sweepRunning reports whether a pipeline sweep of the given name (bugs or
// security) is live for any registered project: its state file must report
// running/triaging and be fresher than findbugsHuntWindow, so a crashed sweep
// stops pinning the pane's indicator. Shared by both panes' hunt probes.
func sweepRunning(name string) bool {
	for _, p := range mockupRoots() {
		w := pipeline.Workspace{Dir: p.Dir, Name: name}
		status, err := w.StateGet("status")
		if err != nil || (status != "running" && status != "triaging") {
			continue
		}
		if fi, statErr := os.Stat(w.StatePath()); statErr == nil && time.Since(fi.ModTime()) < findbugsHuntWindow {
			return true
		}
	}
	return false
}

// findbugsHuntWindow bounds how long a running/triaging status counts as an
// active hunt; past it a crashed sweep no longer pins the pane's indicator.
const findbugsHuntWindow = 60 * time.Minute

// CreateMocks asks the daemon to launch the human-mockups skill for one PM
// ticket — the same containerized agent path as GenerateFeatures. It returns
// once the agent is launched; the card's mockupState reflects progress on the
// next Cards() reconcile.
func (a *App) CreateMocks(pmKey, pmTitle, description string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.CreateMocks(info.Addr, info.Token, daemon.CreateMocksRequest{
		PMKey:       pmKey,
		PMTitle:     pmTitle,
		Description: description,
	}))
}

// CreateVariations asks the daemon to spawn a new group of variations of one
// existing mockup (parentSlug/parentFile) honoring the free-text instructions.
// The source group is never touched; the new group attaches under it in the
// tree. Returns once the agent is launched, like CreateMocks.
func (a *App) CreateVariations(pmKey, feature, parentSlug, parentFile, instructions string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.CreateVariations(info.Addr, info.Token, daemon.CreateVariationsRequest{
		PMKey:        pmKey,
		Feature:      feature,
		ParentSlug:   parentSlug,
		ParentFile:   parentFile,
		Instructions: instructions,
	}))
}

// ChooseMockup marks a leaf mockup as the ticket's winner; an empty slug clears
// the current choice. Host-local state (never the tracker), consistent with the
// mockup link.
func (a *App) ChooseMockup(pmKey, slug, file string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.ChooseMockup(info.Addr, info.Token, daemon.ChooseMockupRequest{
		PMKey: pmKey,
		Slug:  slug,
		File:  file,
	}))
}

// PruneMockup archives a variation group and its descendants; the root group of
// a ticket cannot be pruned. If the current winner lives in the pruned subtree
// the daemon clears it.
func (a *App) PruneMockup(pmKey, slug string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.PruneMockup(info.Addr, info.Token, daemon.PruneMockupRequest{
		PMKey: pmKey,
		Slug:  slug,
	}))
}

// CloseTicket closes a PM ticket (transitions it to Done) via the daemon's
// dedicated close-ticket route. Like Transition it goes through the daemon, so
// the close is prompt-free — it never hits the interactive `issue status`
// confirmation. The board's own drag-and-confirm dialog is the user's consent.
func (a *App) CloseTicket(pmKey string) error {
	info, err := daemon.ReadInfo()
	if err != nil {
		return err
	}
	return daemonCause(daemon.CloseTicket(info.Addr, info.Token, daemon.CloseTicketRequest{PMKey: pmKey}))
}

// IdeationMsg is the frontend-facing transcript entry.
type IdeationMsg struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// IdeationOption is one guided-mode multiple-choice question, frontend-facing.
type IdeationOption struct {
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Kind    string   `json:"kind"`
}

// IdeationDraftView is the frontend-facing agent-drafted ticket summary.
type IdeationDraftView struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// IdeationView is the frontend-facing session snapshot.
type IdeationView struct {
	SessionID  string             `json:"sessionId,omitempty"`
	Mode       string             `json:"mode,omitempty"`
	State      string             `json:"state"`
	Messages   []IdeationMsg      `json:"messages"`
	Question   *IdeationOption    `json:"question,omitempty"`
	Draft      *IdeationDraftView `json:"draft,omitempty"`
	CreatedKey string             `json:"createdKey,omitempty"`
	Error      string             `json:"error,omitempty"`
}

// StartIdeation begins (or re-attaches to) the board ideation session. mode
// is "chat" or "guided"; empty defaults to "chat" in the daemon engine.
// evolveKey (with the card's idea labels) switches the session to evolve
// mode: the outcome rewrites that ticket in place instead of creating one —
// the Ideas→Backlog promotion path.
func (a *App) StartIdeation(seed, mode string, restart bool, evolveKey string, evolveLabels []string) (IdeationView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IdeationView{}, err
	}
	st, err := daemon.IdeationStart(info.Addr, info.Token, daemon.IdeationStartRequest{
		Seed:         seed,
		Mode:         daemon.IdeationMode(mode),
		Restart:      restart,
		EvolveKey:    evolveKey,
		EvolveLabels: evolveLabels,
	})
	if err != nil {
		return IdeationView{}, daemonCause(err)
	}
	return ideationView(st), nil
}

// CreateIdea quick-captures a title-only idea ticket — the Ideas column's `+`.
// Returns the created ticket's key so the frontend can reconcile the
// optimistic placeholder by key instead of title (SC-1691).
func (a *App) CreateIdea(title string) (string, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return "", err
	}
	resp, err := daemon.IdeaCreate(info.Addr, info.Token, daemon.IdeaCreateRequest{Title: title})
	if err != nil {
		return "", daemonCause(err)
	}
	return resp.Key, nil
}

// CreateBug files a defect ticket from the Bugs pane's `+` dialog. The daemon
// marks it as a bug the way the PM tracker natively understands, so the card
// lands in the bug grid on every backend. Returns the created ticket's key so
// the frontend can reconcile the optimistic placeholder by key instead of
// title (SC-1691).
func (a *App) CreateBug(title, description string) (string, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return "", err
	}
	resp, err := daemon.BugCreate(info.Addr, info.Token, daemon.BugCreateRequest{Title: title, Description: description})
	if err != nil {
		return "", daemonCause(err)
	}
	return resp.Key, nil
}

// CreateSecurity files a security ticket from the Security section's `+` dialog.
// The daemon marks it with the security label so the card lands in the Security
// half on every backend. Returns the created ticket's key so the frontend can
// reconcile the optimistic placeholder by key instead of title (SC-1691).
func (a *App) CreateSecurity(title, description string) (string, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return "", err
	}
	resp, err := daemon.SecurityCreate(info.Addr, info.Token, daemon.SecurityCreateRequest{Title: title, Description: description})
	if err != nil {
		return "", daemonCause(err)
	}
	return resp.Key, nil
}

// ReplyIdeation sends the user's answer into the running session.
func (a *App) ReplyIdeation(sessionID, message string) (IdeationView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IdeationView{}, err
	}
	st, err := daemon.IdeationReply(info.Addr, info.Token, daemon.IdeationReplyRequest{SessionID: sessionID, Message: message})
	if err != nil {
		return IdeationView{}, daemonCause(err)
	}
	return ideationView(st), nil
}

// ApproveIdeation submits the user's (possibly edited) guided-mode draft for
// ticket creation.
func (a *App) ApproveIdeation(sessionID, title, description string) (IdeationView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IdeationView{}, err
	}
	st, err := daemon.IdeationApprove(info.Addr, info.Token, daemon.IdeationApproveRequest{
		SessionID:   sessionID,
		Title:       title,
		Description: description,
	})
	if err != nil {
		return IdeationView{}, daemonCause(err)
	}
	return ideationView(st), nil
}

// IdeationStatus returns the current session snapshot for panel polling and
// re-attach on panel reopen. Re-attach (rather than treating panel close as
// abandonment) is the deliberate AD-4 lifecycle: closing the panel does not
// stop the daemon-side session, so reopening must recover the live transcript.
func (a *App) IdeationStatus() (IdeationView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return IdeationView{}, err
	}
	st, err := daemon.GetIdeationStatus(info.Addr, info.Token)
	if err != nil {
		return IdeationView{}, daemonCause(err)
	}
	return ideationView(st), nil
}

// ideationView maps the daemon wire snapshot to the frontend-facing shape.
func ideationView(st daemon.IdeationStatus) IdeationView {
	view := IdeationView{
		SessionID:  st.SessionID,
		Mode:       string(st.Mode),
		State:      string(st.State),
		Messages:   []IdeationMsg{},
		CreatedKey: st.CreatedKey,
		Error:      st.Error,
	}
	if st.Question != nil {
		view.Question = &IdeationOption{Text: st.Question.Text, Options: st.Question.Options, Kind: st.Question.Kind}
	}
	if st.Draft != nil {
		view.Draft = &IdeationDraftView{Title: st.Draft.Title, Description: st.Draft.Description}
	}
	for _, m := range st.Transcript {
		view.Messages = append(view.Messages, IdeationMsg{Role: m.Role, Text: m.Text})
	}
	return view
}

// DescEditMsg is the frontend-facing description-edit chat transcript entry.
type DescEditMsg struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// DescEditView is the frontend-facing description-edit session snapshot.
type DescEditView struct {
	SessionID  string        `json:"sessionId,omitempty"`
	Key        string        `json:"key,omitempty"`
	State      string        `json:"state"`
	Messages   []DescEditMsg `json:"messages"`
	Proposal   string        `json:"proposal,omitempty"`
	AppliedURL string        `json:"appliedUrl,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// StartDescEdit begins (or re-attaches to) the Product-Backlog
// description-edit chat for one ticket. currentDescription seeds the
// agent's context — the frontend fetches it via GetIssueDetail before
// calling this, since some trackers' list fetches omit the description.
func (a *App) StartDescEdit(key, currentDescription string, restart bool) (DescEditView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return DescEditView{}, err
	}
	st, err := daemon.DescEditStart(info.Addr, info.Token, daemon.DescEditStartRequest{
		Key:                key,
		CurrentDescription: currentDescription,
		Restart:            restart,
	})
	if err != nil {
		return DescEditView{}, daemonCause(err)
	}
	return descEditView(st), nil
}

// ReplyDescEdit sends the user's chat message into the running
// description-edit session.
func (a *App) ReplyDescEdit(sessionID, message string) (DescEditView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return DescEditView{}, err
	}
	st, err := daemon.DescEditReply(info.Addr, info.Token, daemon.DescEditReplyRequest{SessionID: sessionID, Message: message})
	if err != nil {
		return DescEditView{}, daemonCause(err)
	}
	return descEditView(st), nil
}

// ApplyDescEdit writes the session's current proposed rewrite to the
// tracker — the modal's explicit Apply/Save action; nothing is written
// before this call.
func (a *App) ApplyDescEdit(sessionID string) (DescEditView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return DescEditView{}, err
	}
	st, err := daemon.DescEditApply(info.Addr, info.Token, daemon.DescEditApplyRequest{SessionID: sessionID})
	if err != nil {
		return DescEditView{}, daemonCause(err)
	}
	return descEditView(st), nil
}

// DescEditStatus returns the current description-edit session snapshot, for
// modal-open re-attach and in-flight-turn polling.
func (a *App) DescEditStatus() (DescEditView, error) {
	info, err := daemon.ReadInfo()
	if err != nil {
		return DescEditView{}, err
	}
	st, err := daemon.GetDescEditStatus(info.Addr, info.Token)
	if err != nil {
		return DescEditView{}, daemonCause(err)
	}
	return descEditView(st), nil
}

// descEditView maps the daemon wire snapshot to the frontend-facing shape.
func descEditView(st daemon.DescEditStatus) DescEditView {
	view := DescEditView{
		SessionID:  st.SessionID,
		Key:        st.Key,
		State:      string(st.State),
		Messages:   []DescEditMsg{},
		Proposal:   st.Proposal,
		AppliedURL: st.AppliedURL,
		Error:      st.Error,
	}
	for _, m := range st.Transcript {
		view.Messages = append(view.Messages, DescEditMsg{Role: m.Role, Text: m.Text})
	}
	return view
}
