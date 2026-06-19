package cmdtui

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/browser"
	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/claude/logparser"
	"github.com/gethuman-sh/human/internal/claude/monitor"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/logo"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
)

const defaultWidth = 80

// trackerIssues groups issues from one tracker instance and project.
type trackerIssues struct {
	TrackerName       string
	TrackerKind       string
	TrackerRole       string // "pm", "engineering", or empty
	Project           string
	Issues            []tracker.Issue
	ReadyForReview    map[string]bool   // issue keys currently flagged ready for review (see CLAUDE.md Review handoff)
	ReadyForReviewPRs map[string]string // issue key -> pull-request URL from the handoff's pr: line, when present
	Err               error
}

// flatIssue is a single issue with its tracker context, used for cursor indexing.
type flatIssue struct {
	TrackerKind string
	Project     string
	Issue       tracker.Issue
}

// flattenIssues flattens grouped tracker issues into a single slice for cursor navigation.
func flattenIssues(groups []trackerIssues) []flatIssue {
	var out []flatIssue
	for _, g := range groups {
		if g.Err != nil || len(g.Issues) == 0 {
			continue
		}
		for _, issue := range g.Issues {
			out = append(out, flatIssue{TrackerKind: g.TrackerKind, Project: g.Project, Issue: issue})
		}
	}
	return out
}

// BuildTuiCmd creates the "tui" command.
func BuildTuiCmd() *cobra.Command {
	var projectDirs []string
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Interactive dashboard for Claude Code usage",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runTUI(projectDirs)
		},
	}
	cmd.Flags().StringArrayVar(&projectDirs, "project", nil, "Project directory to register (repeatable; forwarded to daemon)")
	return cmd
}

func runTUI(projectDirs []string) error {
	// Suppress log output while the TUI owns the terminal.
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.Disabled)
	defer zerolog.SetGlobalLevel(prev)

	ensureDaemon(projectDirs)
	finder, dc := buildFinder()
	mon := monitor.New(finder, dc)
	m := newModel(mon)
	if m.daemonUnsub != nil {
		defer m.daemonUnsub()
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// ensureDaemon starts the daemon if it is not already running.
// When projectDirs is non-empty, the --project flags are forwarded to the daemon.
func ensureDaemon(projectDirs []string) {
	if _, alive := daemon.ReadAlivePid(); alive {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	args := []string{"daemon", "start",
		"--addr", "0.0.0.0:19285",
		"--chrome-addr", "0.0.0.0:19286",
		"--proxy-addr", "0.0.0.0:19287",
	}
	for _, dir := range projectDirs {
		args = append(args, "--project", dir)
	}
	child := exec.Command(exe, args...) // #nosec G204 -- re-exec of own binary via os.Executable()
	child.Stdout = nil
	child.Stderr = nil
	_ = child.Start()
	if child.Process != nil {
		_ = child.Process.Release()
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", "localhost:19285", 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func buildFinder() (claude.InstanceFinder, claude.DockerClient) {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Debug().Err(err).Msg("cannot resolve home dir for host finder")
		home = ""
	}
	finders := []claude.InstanceFinder{
		&claude.HostFinder{Runner: claude.OSCommandRunner{}, HomeDir: home},
	}
	var dc claude.DockerClient
	if client, dcErr := claude.NewEngineDockerClient(); dcErr == nil {
		dc = client
		finders = append(finders, &claude.DockerFinder{Client: dc})
	}
	return &claude.CombinedFinder{Finders: finders}, dc
}

// --- bubbletea model ---

type model struct {
	mon          *monitor.Monitor
	snap         *monitor.Snapshot
	spinner      spinner.Model
	width        int
	height       int
	quitting     bool
	fetchGen     uint64                       // monotonic counter; assigned when dispatching a fetch
	fetching     bool                         // true while a fetch command is in flight
	showSplash   bool                         // true during the initial logo display period
	logMode      string                       // traffic log mode: "off", "meta", "full"
	daemonEvents <-chan daemon.SubscribeEvent // push notifications from daemon
	daemonUnsub  func()                       // closes daemon subscription

	issues        []trackerIssues // issues from configured tracker projects
	issuesLoading bool            // true while issue fetch is in flight
	issuesFetched time.Time       // when issues were last successfully fetched

	issueCursor    int       // index into flattenIssues() result
	dispatching    bool      // true while dispatch cmd is in-flight
	dispatchStatus string    // feedback flash: "Sent HUM-42 → session:0.1"
	dispatchAt     time.Time // when status was set; auto-cleared after 3s

	prevStatuses map[string]logparser.SessionStatus // previous session statuses for idle detection

	projects  []daemon.ProjectInfo // registered projects from daemon info
	activeTab int                  // index into tabs(); 0 = first project or "All"

	// Destructive operation confirmation overlay.
	pendingConfirms []daemon.PendingConfirm // from daemon polling
	confirmActive   bool                    // true when confirm overlay is shown
	confirmID       string                  // which pending op we're showing
	confirmPrompt   string                  // e.g. "DeleteIssue KAN-1?"
	confirmPIDs     map[int]bool            // PIDs of Claude instances with pending confirms

	// Create ticket form overlay.
	createActive  bool      // true when the create form is shown
	createForm    *huh.Form // the huh form (implements tea.Model)
	createTracker int       // selected tracker index (value bound to form)
	// createOptions captures the tracker options list at form-creation
	// time so the resolved selection is stable against refreshes of
	// m.issues while the form is open.
	createOptions []trackerOption
	createTitle   string // bound to form
	createDesc    string // bound to form
}

func newModel(mon *monitor.Monitor) model {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(humanRed)
	m := model{mon: mon, spinner: sp, width: defaultWidth, fetchGen: 1, fetching: true, showSplash: true, logMode: "off", prevStatuses: make(map[string]logparser.SessionStatus)}
	// Subscribe to daemon push notifications (best-effort; falls back to polling).
	if info, err := daemon.ReadInfo(); err == nil && info.Addr != "" {
		if ch, unsub, subErr := daemon.Subscribe(info.Addr, info.Token); subErr == nil {
			m.daemonEvents = ch
			m.daemonUnsub = unsub
		}
	}
	return m
}

// --- messages ---

type fullTickMsg time.Time
type splashDoneMsg struct{} // fired after the splash logo display period
type daemonEventMsg struct {
	event daemon.SubscribeEvent
}

type snapshotMsg struct {
	snap     *monitor.Snapshot
	gen      uint64
	skeleton bool // true when this is a fast skeleton that needs a heavy follow-up
}

type logModeMsg string                // result of log-mode set/get from daemon
type projectsMsg []daemon.ProjectInfo // projects loaded from daemon info

type issueTickMsg time.Time
type issuesResultMsg struct {
	results []trackerIssues
}

type pendingConfirmsMsg []daemon.PendingConfirm
type confirmDecisionMsg struct{ err error }
type createResultMsg struct {
	key         string
	trackerKind string
	err         error
}

// --- tea.Model ---

func (m model) Init() tea.Cmd {
	splashTimer := tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return splashDoneMsg{} })
	return tea.Batch(fetchSkeleton(m.mon, 1), splashTimer, m.spinner.Tick, fullTickCmd(), listenDaemonEvents(m.daemonEvents), fetchLogModeCmd(), fetchIssuesCmd(), issueTickCmd(), fetchProjectsCmd())
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// When a destructive confirmation overlay is active, only handle y/n/Esc.
	if m.confirmActive {
		return m.handleConfirmKey(msg)
	}

	// When the create form is active, delegate all keys to it.
	if m.createActive {
		return m.handleCreateKey(msg)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "l":
		next := cycleLogMode(m.logMode)
		m.logMode = next
		return m, setLogModeCmd(next)
	case "j", "down":
		flat := flattenIssues(m.issues)
		if len(flat) > 0 {
			m.issueCursor = min(m.issueCursor+1, len(flat)-1)
		}
		return m, nil
	case "k", "up":
		if m.issueCursor > 0 {
			m.issueCursor--
		}
		return m, nil
	case "enter":
		return m.handleDispatch()
	case "o":
		return m.handleOpenBrowser()
	case "n":
		return m.handleCreateStart()
	case "a":
		return m.handleSpawnAgent()
	default:
		return m.handleTabKey(msg)
	}
}

func (m model) handleTabKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		tabs := m.tabs()
		if len(tabs) > 1 {
			m.activeTab = (m.activeTab + 1) % len(tabs)
		}
	case "shift+tab":
		tabs := m.tabs()
		if len(tabs) > 1 {
			m.activeTab = (m.activeTab - 1 + len(tabs)) % len(tabs)
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(msg.Runes[0]-'0') - 1 // "1" → 0
		tabs := m.tabs()
		if idx < len(tabs) {
			m.activeTab = idx
		}
	}
	return m, nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)
	case projectsMsg:
		m.applyProjects(msg)
	case tea.WindowSizeMsg:
		m.applyWindowSize(msg)
	case fullTickMsg:
		return m.handleFullTick()
	case snapshotMsg:
		return m.handleSnapshot(msg)
	case splashDoneMsg:
		m.showSplash = false
	case daemonEventMsg:
		return m.handleDaemonEvent(msg)
	case issueTickMsg:
		return m.handleIssueTick()
	case spawnAgentMsg:
		return m.handleSpawnAgentResult(msg)
	case issuesResultMsg, dispatchResultMsg, openBrowserMsg, pendingConfirmsMsg, confirmDecisionMsg:
		m.handleResultMsg(msg)
	case createResultMsg:
		return m.handleCreateResult(msg)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	default:
		return m.handleDefault(msg)
	}
	return m, nil
}

// handleDaemonEvent reacts to a push notification from the daemon.
// For agent-stopped events, the instance is removed from the snapshot
// immediately so the TUI doesn't wait for the next discovery cycle.
func (m model) handleDaemonEvent(msg daemonEventMsg) (tea.Model, tea.Cmd) {
	listen := listenDaemonEvents(m.daemonEvents)

	// Immediate removal for stopped agents. Container names follow the
	// pattern "human-agent-<name>", which appears quoted in the label.
	if msg.event.Type == "agent-stopped" && msg.event.AgentName != "" && m.snap != nil {
		containerName := "human-agent-" + msg.event.AgentName
		filtered := m.snap.Instances[:0:0]
		for _, iv := range m.snap.Instances {
			if !strings.Contains(iv.Usage.Instance.Label, containerName) {
				filtered = append(filtered, iv)
			}
		}
		m.snap.Instances = filtered
	}

	if m.fetching {
		return m, listen
	}
	m.fetching = true
	m.fetchGen++
	return m, tea.Batch(fetchFull(m.mon, m.fetchGen), listen)
}

func (m model) handleFullTick() (tea.Model, tea.Cmd) {
	m.clearExpiredDispatchStatus()
	if m.fetching {
		return m, fullTickCmd()
	}
	m.fetching = true
	m.fetchGen++
	return m, tea.Batch(fetchFull(m.mon, m.fetchGen), fullTickCmd(), fetchProjectsCmd(), fetchPendingConfirmsCmd())
}

func (m model) handleSnapshot(msg snapshotMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.fetchGen {
		return m, nil // stale result, discard
	}
	m.snap = msg.snap
	m.fetching = false
	m.checkIdleTransitions()

	// Skeleton rendered instantly; now chain the heavy fetch to fill in
	// instances, trackers, tool stats, and network events.
	if msg.skeleton {
		m.fetching = true
		return m, fetchHeavy(m.mon, msg.snap, msg.gen)
	}
	// Heavy fetch done; show the dashboard even if splash timer hasn't fired.
	m.showSplash = false
	return m, nil
}

func (m *model) handleConfirmDecision(msg confirmDecisionMsg) {
	if msg.err != nil {
		m.dispatchStatus = fmt.Sprintf("Confirm failed: %s", msg.err)
		m.dispatchAt = time.Now()
	}
}

// checkIdleTransitions plays a notification sound when any instance
// transitions from working/blocked/waiting to ready (idle).
func (m model) checkIdleTransitions() {
	if m.snap == nil {
		return
	}
	bing := false
	current := make(map[string]logparser.SessionStatus, len(m.snap.Instances))
	for _, iv := range m.snap.Instances {
		if iv.Session == nil {
			continue
		}
		sid := iv.Session.SessionID
		cur := iv.Session.Status
		current[sid] = cur
		prev, known := m.prevStatuses[sid]
		if !known {
			continue
		}
		if cur == logparser.StatusReady && (prev == logparser.StatusWorking || prev == logparser.StatusBlocked || prev == logparser.StatusWaiting) {
			bing = true
		}
	}
	for k := range m.prevStatuses {
		delete(m.prevStatuses, k)
	}
	for k, v := range current {
		m.prevStatuses[k] = v
	}
	if bing {
		playNotificationSound()
	}
}

// handleFastTick processes the fast (100ms) tick for quick snapshot refreshes.
// applyProjects updates the project list and clamps the active tab.
func (m *model) applyProjects(projects projectsMsg) {
	m.projects = []daemon.ProjectInfo(projects)
	if m.activeTab >= len(m.tabs()) {
		m.activeTab = 0
	}
}

// clampCursor keeps issueCursor in bounds after the issue list changes.
func (m *model) clampCursor() {
	flat := flattenIssues(m.issues)
	if m.issueCursor >= len(flat) {
		m.issueCursor = max(0, len(flat)-1)
	}
}

// clearExpiredDispatchStatus clears the dispatch status flash after 3 seconds.
func (m *model) clearExpiredDispatchStatus() {
	if !m.dispatchAt.IsZero() && time.Since(m.dispatchAt) > 3*time.Second {
		m.dispatchStatus = ""
		m.dispatchAt = time.Time{}
	}
}

// --- dispatch ---

type dispatchResultMsg struct {
	issueKey  string
	agentName string
	err       error
}

func (m *model) handleDispatchResult(msg dispatchResultMsg) {
	m.dispatching = false
	if msg.err != nil {
		m.dispatchStatus = fmt.Sprintf("Failed: %s", msg.err)
	} else {
		m.dispatchStatus = fmt.Sprintf("Spawned %s for %s", msg.agentName, msg.issueKey)
	}
	m.dispatchAt = time.Now()
}

func (m model) handleDispatch() (tea.Model, tea.Cmd) {
	if m.dispatching {
		return m, nil
	}
	flat := flattenIssues(m.issues)
	if len(flat) == 0 {
		m.dispatchStatus = "No issues"
		m.dispatchAt = time.Now()
		return m, nil
	}
	if m.issueCursor >= len(flat) {
		return m, nil
	}
	if os.Getenv("TMUX") == "" {
		m.dispatchStatus = "Not in tmux"
		m.dispatchAt = time.Now()
		return m, nil
	}
	sel := flat[m.issueCursor]

	// Bug-ness wins regardless of tracker — a Shortcut bug story still wants
	// root-cause analysis, not the generic planner. Otherwise the PM/eng
	// split decides: Shortcut tickets are PM and want planning; everything
	// else is engineering and goes straight to execution.
	var prompt string
	switch {
	case sel.Issue.IsBug():
		prompt = "/human-bug-plan " + sel.Issue.Key
	case sel.TrackerKind == "shortcut":
		prompt = "/human-plan " + sel.Issue.Key
	default:
		prompt = "/human-execute " + sel.Issue.Key
	}

	name := nextAgentName()
	projectDir := m.activeProjectDir()
	var tmuxTarget string
	if m.snap != nil {
		for _, p := range m.filterPanes(m.snap.Panes) {
			tmuxTarget = fmt.Sprintf("%s:%d", p.SessionName, p.WindowIndex)
			break
		}
	}

	m.dispatching = true
	m.dispatchStatus = fmt.Sprintf("Spawning %s for %s...", name, sel.Issue.Key)
	m.dispatchAt = time.Now()
	return m, dispatchIssueCmd(name, projectDir, tmuxTarget, prompt, sel.Issue.Key)
}

func dispatchIssueCmd(name, projectDir, tmuxTarget, prompt, issueKey string) tea.Cmd {
	return func() tea.Msg {
		err := spawnAgentInTmux(name, projectDir, tmuxTarget, false, "--prompt", prompt, "--skip-permissions")
		return dispatchResultMsg{issueKey: issueKey, agentName: name, err: err}
	}
}

// spawnAgentInTmux splits a new tmux pane and starts a human agent there.
// stopOnExit governs cleanup: interactive sessions block in the pane until
// Claude exits and then want the container torn down, whereas dispatched
// (--prompt) agents launch detached and must keep running after the spawning
// command returns.
func spawnAgentInTmux(name, projectDir, tmuxTarget string, stopOnExit bool, extraFlags ...string) error {
	humanExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find executable: %w", err)
	}
	parts := []string{humanExe, "agent", "start", name}
	parts = append(parts, extraFlags...)
	paneDir, _ := os.Getwd()
	if projectDir != "" {
		parts = append(parts, "--workspace", projectDir)
		paneDir = projectDir
	}
	// The pane command is interpreted by a shell, so every token must be quoted.
	// The dispatch prompt (e.g. "/human-execute HUM-42") contains a space; left
	// unquoted the shell splits it and `agent start` sees an extra positional
	// argument, failing with "accepts 1 arg(s), received 2".
	cmd := buildPaneCmd(shellJoin(parts), shellQuote(humanExe), name, stopOnExit)
	tmuxArgs := []string{"split-window", "-h", "-c", paneDir}
	if tmuxTarget != "" {
		tmuxArgs = append(tmuxArgs, "-t", tmuxTarget)
	}
	tmuxArgs = append(tmuxArgs, cmd)
	runner := claude.OSCommandRunner{}
	if _, err = runner.Run(context.Background(), "tmux", tmuxArgs...); err != nil {
		return err
	}
	// Re-balance pane widths so repeated spawns don't drift the window
	// into a lopsided layout (split-window only halves the active pane).
	layoutTarget := tmuxTarget
	if layoutTarget == "" {
		layoutTarget = "."
	}
	_, _ = runner.Run(context.Background(), "tmux", "select-layout", "-t", layoutTarget, "even-horizontal")
	return nil
}

// buildPaneCmd assembles the shell line run inside the spawned tmux pane.
// When stopOnExit is set, an `agent stop` follows the agent command so the
// container is torn down once an interactive Claude session ends. Dispatched
// agents run detached, so stopping would kill them the instant they start —
// there the pane only lingers on failure to surface the error.
func buildPaneCmd(agentCmd, humanExe, name string, stopOnExit bool) string {
	const holdOnError = `[ $EC -ne 0 ] && { echo; echo 'Press enter to close'; read; }`
	if stopOnExit {
		return fmt.Sprintf("%s; EC=$?; %s agent stop --async %s 2>/dev/null; %s", agentCmd, humanExe, name, holdOnError)
	}
	return fmt.Sprintf("%s; EC=$?; %s", agentCmd, holdOnError)
}

// shellQuote wraps a token in single quotes so a POSIX shell treats it as one
// literal argument, preserving spaces and metacharacters. Embedded single
// quotes are closed, escaped, and reopened ('\'').
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellJoin shell-quotes each token and joins them with spaces into a single
// command line safe to hand to a shell.
func shellJoin(parts []string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = shellQuote(p)
	}
	return strings.Join(quoted, " ")
}

// --- open in browser ---

type openBrowserMsg struct {
	issueKey string
	err      error
}

func (m model) handleOpenBrowser() (tea.Model, tea.Cmd) {
	flat := flattenIssues(m.issues)
	if len(flat) == 0 || m.issueCursor >= len(flat) {
		return m, nil
	}
	sel := flat[m.issueCursor]
	if sel.Issue.URL == "" {
		m.dispatchStatus = "No URL for " + sel.Issue.Key
		m.dispatchAt = time.Now()
		return m, nil
	}
	return m, openBrowserCmd(sel.Issue.URL, sel.Issue.Key)
}

func (m *model) handleOpenBrowserResult(msg openBrowserMsg) {
	if msg.err != nil {
		m.dispatchStatus = fmt.Sprintf("Open failed: %s", msg.err)
	} else {
		m.dispatchStatus = fmt.Sprintf("Opened %s", msg.issueKey)
	}
	m.dispatchAt = time.Now()
}

func openBrowserCmd(url, issueKey string) tea.Cmd {
	return func() tea.Msg {
		err := browser.DefaultOpener{}.Open(url)
		return openBrowserMsg{issueKey: issueKey, err: err}
	}
}

// --- spawn agent via tmux split ---

type spawnAgentMsg struct {
	name string
	err  error
}

func (m model) handleSpawnAgent() (tea.Model, tea.Cmd) {
	if os.Getenv("TMUX") == "" {
		m.dispatchStatus = "Not in tmux"
		m.dispatchAt = time.Now()
		return m, nil
	}
	name := nextAgentName()
	projectDir := m.activeProjectDir()
	// Find a tmux window belonging to this project so the agent pane
	// opens next to the project's existing Claude instances.
	var tmuxTarget string
	if m.snap != nil {
		for _, p := range m.filterPanes(m.snap.Panes) {
			tmuxTarget = fmt.Sprintf("%s:%d", p.SessionName, p.WindowIndex)
			break
		}
	}
	m.dispatchStatus = fmt.Sprintf("Spawning %s...", name)
	m.dispatchAt = time.Now()
	// Inject a placeholder so the instance appears immediately while the
	// devcontainer builds and Claude starts up.
	if m.snap != nil {
		m.snap.Instances = append(m.snap.Instances, monitor.InstanceView{
			Usage: claude.InstanceUsage{
				Instance: claude.Instance{
					Label:  fmt.Sprintf("Container %q (starting...)", "human-agent-"+name),
					Source: "container",
					Cwd:    projectDir,
				},
			},
		})
	}
	return m, spawnAgentCmd(name, projectDir, tmuxTarget)
}

// activeProjectDir returns the directory of the active project tab.
func (m model) activeProjectDir() string {
	tabs := m.tabs()
	if m.activeTab < len(tabs) && tabs[m.activeTab].Dir != "" {
		return tabs[m.activeTab].Dir
	}
	if len(m.projects) > 0 {
		return m.projects[0].Dir
	}
	return ""
}

func (m model) handleSpawnAgentResult(msg spawnAgentMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.dispatchStatus = fmt.Sprintf("Spawn failed: %s", msg.err)
		m.dispatchAt = time.Now()
		return m, nil
	}
	m.dispatchStatus = fmt.Sprintf("Spawned %s", msg.name)
	m.dispatchAt = time.Now()
	// Force immediate full discovery so the new instance appears quickly.
	m.fetching = true
	m.fetchGen++
	return m, fetchFull(m.mon, m.fetchGen)
}

func spawnAgentCmd(name, projectDir, tmuxTarget string) tea.Cmd {
	return func() tea.Msg {
		err := spawnAgentInTmux(name, projectDir, tmuxTarget, true, "--interactive", "--skip-permissions")
		return spawnAgentMsg{name: name, err: err}
	}
}

// nextAgentName returns agent-1, agent-2, etc. based on existing agent metadata.
func nextAgentName() string {
	metas, err := agent.ListMetas()
	if err != nil {
		return fmt.Sprintf("agent-%d", time.Now().Unix())
	}
	maxN := 0
	for _, m := range metas {
		if strings.HasPrefix(m.Name, "agent-") {
			if n, parseErr := strconv.Atoi(strings.TrimPrefix(m.Name, "agent-")); parseErr == nil && n > maxN {
				maxN = n
			}
		}
	}
	return fmt.Sprintf("agent-%d", maxN+1)
}

// --- destructive confirmation overlay ---

func (m model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		id := m.confirmID
		m.confirmActive = false
		m.confirmID = ""
		m.confirmPrompt = ""
		m.dispatchStatus = "Approved"
		m.dispatchAt = time.Now()
		return m, sendConfirmCmd(id, true)
	case "n", "esc":
		id := m.confirmID
		m.confirmActive = false
		m.confirmID = ""
		m.confirmPrompt = ""
		m.dispatchStatus = "Aborted"
		m.dispatchAt = time.Now()
		return m, sendConfirmCmd(id, false)
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil // swallow all other keys while confirming
}

func (m *model) handlePendingConfirms(confirms []daemon.PendingConfirm) {
	m.pendingConfirms = confirms

	// Build PID set for instance state rendering.
	m.confirmPIDs = make(map[int]bool, len(confirms))
	for _, c := range confirms {
		if c.ClientPID > 0 {
			m.confirmPIDs[c.ClientPID] = true
		}
	}

	if len(confirms) > 0 && !m.confirmActive {
		m.confirmActive = true
		m.confirmID = confirms[0].ID
		m.confirmPrompt = confirms[0].Prompt
	}
	if len(confirms) == 0 && m.confirmActive {
		m.confirmActive = false
		m.confirmID = ""
		m.confirmPrompt = ""
	}
}

func sendConfirmCmd(id string, approved bool) tea.Cmd {
	return func() tea.Msg {
		addr, token := daemonAddr()
		if addr == "" {
			return confirmDecisionMsg{err: fmt.Errorf("daemon not available")}
		}
		err := daemon.SendConfirmDecision(addr, token, id, approved)
		return confirmDecisionMsg{err: err}
	}
}

func fetchPendingConfirmsCmd() tea.Cmd {
	return func() tea.Msg {
		addr, token := daemonAddr()
		if addr == "" {
			return pendingConfirmsMsg(nil)
		}
		confirms, err := daemon.GetPendingConfirms(addr, token)
		if err != nil {
			return pendingConfirmsMsg(nil)
		}
		return pendingConfirmsMsg(confirms)
	}
}

// --- create ticket form ---

// trackerOption is a unique tracker/project pair for the create form selector.
type trackerOption struct {
	Kind    string
	Role    string
	Project string
}

// applyWindowSize updates the terminal dimensions.
func (m *model) applyWindowSize(msg tea.WindowSizeMsg) {
	m.width = msg.Width
	m.height = msg.Height
}

// handleDefault routes unmatched messages: logModeMsg, and create form delegation.
func (m *model) handleResultMsg(msg tea.Msg) {
	switch msg := msg.(type) {
	case issuesResultMsg:
		m.handleIssuesResult(msg)
	case dispatchResultMsg:
		m.handleDispatchResult(msg)
	case openBrowserMsg:
		m.handleOpenBrowserResult(msg)
	case pendingConfirmsMsg:
		m.handlePendingConfirms(msg)
	case confirmDecisionMsg:
		m.handleConfirmDecision(msg)
	}
}

func (m model) handleDefault(msg tea.Msg) (tea.Model, tea.Cmd) {
	if lm, ok := msg.(logModeMsg); ok {
		m.logMode = string(lm)
		return m, nil
	}
	if m.createActive && m.createForm != nil {
		return m.updateCreateForm(msg)
	}
	return m, nil
}

// handleCreateStart builds the create form from loaded tracker/project pairs and activates it.
func (m model) handleCreateStart() (tea.Model, tea.Cmd) {
	// Extract unique (kind, role, project) tuples from loaded issues.
	seen := make(map[trackerOption]bool)
	var options []trackerOption
	for _, g := range m.issues {
		role := g.TrackerRole
		if role == "" {
			role = inferRole(g.TrackerKind)
		}
		opt := trackerOption{Kind: g.TrackerKind, Role: role, Project: g.Project}
		if !seen[opt] {
			seen[opt] = true
			options = append(options, opt)
		}
	}
	if len(options) == 0 {
		m.dispatchStatus = "No trackers"
		m.dispatchAt = time.Now()
		return m, nil
	}

	// Default to first PM tracker. Cache the options list so the submit
	// path resolves the selection against the same slice even if
	// m.issues is refreshed while the form is open.
	m.createTracker = 0
	for i, opt := range options {
		if opt.Role == "pm" {
			m.createTracker = i
			break
		}
	}
	m.createOptions = options
	m.createTitle = ""
	m.createDesc = ""

	selectOptions := make([]huh.Option[int], len(options))
	for i, opt := range options {
		selectOptions[i] = huh.NewOption(fmt.Sprintf("%s / %s", opt.Kind, opt.Project), i)
	}
	fields := []huh.Field{
		huh.NewSelect[int]().
			Title("Tracker").
			Options(selectOptions...).
			Value(&m.createTracker).
			Inline(true),
	}

	fields = append(fields,
		huh.NewInput().
			Title("Title").
			Value(&m.createTitle).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("title is required")
				}
				return nil
			}),
		huh.NewText().
			Title("Description").
			Value(&m.createDesc).
			Lines(5),
	)

	dialogWidth := min(60, m.width-10)
	m.createForm = huh.NewForm(huh.NewGroup(fields...)).WithTheme(huh.ThemeCharm()).WithWidth(dialogWidth)
	m.createActive = true

	return m, m.createForm.Init()
}

// handleCreateKey delegates key messages to the create form.
func (m model) handleCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		m.createActive = false
		m.createForm = nil
		return m, nil
	}
	return m.updateCreateForm(msg)
}

// updateCreateForm passes a message to the create form and checks its state.
func (m model) updateCreateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	form, cmd := m.createForm.Update(msg)
	m.createForm = form.(*huh.Form)

	if m.createForm.State == huh.StateCompleted {
		m.createActive = false
		m.createForm = nil

		// Resolve the selected tracker/project against the snapshot
		// captured at form-creation time. Rebuilding the list here
		// from m.issues would desync the index from the labels the
		// user actually saw.
		options := m.createOptions
		m.createOptions = nil
		if m.createTracker >= 0 && m.createTracker < len(options) {
			sel := options[m.createTracker]
			return m, createTicketCmd(sel.Kind, sel.Project, m.createTitle, m.createDesc)
		}
		m.dispatchStatus = "Create failed: invalid tracker"
		m.dispatchAt = time.Now()
		return m, nil
	}

	if m.createForm.State == huh.StateAborted {
		m.createActive = false
		m.createForm = nil
		return m, nil
	}

	return m, cmd
}

// handleIssuesResult processes an issues fetch result.
func (m *model) handleIssuesResult(msg issuesResultMsg) {
	m.issues = msg.results
	m.issuesLoading = false
	m.issuesFetched = time.Now()
	m.clampCursor()
}

// handleCreateResult processes the result of a ticket creation.
func (m model) handleCreateResult(msg createResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.dispatchStatus = fmt.Sprintf("Create failed: %s", msg.err)
		m.dispatchAt = time.Now()
		return m, nil
	}

	m.dispatchStatus = fmt.Sprintf("Created %s", msg.key)
	m.dispatchAt = time.Now()
	m.issuesLoading = true

	// Auto-dispatch /human-ready via a new agent.
	if !m.dispatching && os.Getenv("TMUX") != "" {
		name := nextAgentName()
		projectDir := m.activeProjectDir()
		var tmuxTarget string
		if m.snap != nil {
			for _, p := range m.filterPanes(m.snap.Panes) {
				tmuxTarget = fmt.Sprintf("%s:%d", p.SessionName, p.WindowIndex)
				break
			}
		}
		m.dispatching = true
		prompt := "/human-ready " + msg.trackerKind + " " + msg.key
		return m, tea.Batch(fetchIssuesCmd(), dispatchIssueCmd(name, projectDir, tmuxTarget, prompt, msg.key))
	}

	return m, fetchIssuesCmd()
}

func createTicketCmd(trackerKind, project, title, description string) tea.Cmd {
	return func() tea.Msg {
		addr, token := daemonAddr()
		if addr == "" {
			return createResultMsg{err: fmt.Errorf("daemon not available")}
		}
		args := []string{trackerKind, "issue", "create",
			"--project=" + project, title}
		if description != "" {
			args = append(args, "--description", description)
		}
		out, err := daemon.RunRemoteCapture(addr, token, args)
		if err != nil {
			return createResultMsg{err: err}
		}
		key := strings.TrimSpace(string(out))
		if i := strings.IndexByte(key, '\t'); i >= 0 {
			key = key[:i]
		}
		return createResultMsg{key: key, trackerKind: trackerKind}
	}
}

// renderCreateDialog renders the create ticket form inside a centered bordered dialog.
func renderCreateDialog(formView string, width, height int) string {
	title := titleStyle.Render("New Ticket")
	hints := subtleStyle.Render("Tab next  Enter submit  Esc cancel")

	content := title + "\n\n" + formView + "\n" + hints
	dialog := dialogStyle.Render(content)

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, dialog)
}

func (m model) handleIssueTick() (tea.Model, tea.Cmd) {
	if m.issuesLoading {
		return m, issueTickCmd()
	}
	m.issuesLoading = true
	return m, tea.Batch(fetchIssuesCmd(), issueTickCmd())
}

// showingLoading returns true when the TUI should display the logo splash
// screen instead of the dashboard (no snapshot yet, or splash period active).
func (m model) showingLoading() bool {
	return m.snap == nil || m.showSplash
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	w := m.width
	if w < 40 {
		w = defaultWidth
	}

	// Create ticket dialog — skip dashboard rendering entirely.
	if m.createActive && m.createForm != nil {
		return renderCreateDialog(m.createForm.View(), w, m.height)
	}

	// Confirm dialog — skip dashboard rendering entirely.
	if m.confirmActive {
		return renderConfirmDialog(m.confirmPrompt, w, m.height)
	}

	var b strings.Builder

	// Header line: title left, usage window right.
	b.WriteString(m.renderHeader(w))
	b.WriteByte('\n')

	if m.showingLoading() {
		b.WriteByte('\n')
		b.WriteString(logo.Render())
		b.WriteByte('\n')
		b.WriteString("\n  " + m.spinner.View() + " Loading...\n")
		return b.String()
	}

	// Status line: daemon left, telegram right.
	b.WriteString(renderStatusLine(m.snap, w))
	b.WriteByte('\n')

	// Tab bar (only when 2+ projects).
	if tabBar := renderTabBar(m.tabs(), m.activeTab, w); tabBar != "" {
		b.WriteString(tabBar)
		b.WriteByte('\n')
	}

	b.WriteByte('\n')

	if m.snap.Err != nil {
		b.WriteString(errorStyle.Render("  Error: " + m.snap.Err.Error()))
		b.WriteByte('\n')
		return b.String()
	}

	// --- Instances panel ---
	m.renderInstancesAndPanes(&b, w)

	// --- Trackers panel ---
	if ts := renderTrackers(m.snap.Trackers, w); ts != "" {
		b.WriteByte('\n')
		b.WriteString(ts)
		b.WriteByte('\n')
	} else if m.fetching && m.snap.Trackers == nil {
		b.WriteByte('\n')
		b.WriteString("  " + subtleStyle.Render("Trackers") + "  " + m.spinner.View() + subtleStyle.Render(" connecting..."))
		b.WriteByte('\n')
	}

	// Issues panel.
	if ip := renderIssuesPanel(m.issues, m.issuesFetched, w, m.issueCursor); ip != "" {
		b.WriteByte('\n')
		b.WriteString(ip)
	}

	// Tool stats panel.
	if tp := renderToolStatsPanel(m.snap.ToolStats, w); tp != "" {
		b.WriteByte('\n')
		b.WriteString(tp)
	}

	// Domains panel — bottom anchored, fills the gap between the issues
	// panel and the footer. Height is computed from what View() has
	// already emitted, the known footer height (2 lines: blank + footer),
	// and the terminal height. When the terminal is too short to show
	// any rows, the panel renders nothing rather than clipping the
	// footer or the issues panel above it.
	footer := renderFooter(w, m.logMode, m.dispatchStatus, len(m.tabs()) > 0)
	const footerLines = 2 // one blank separator + the footer line itself
	consumed := strings.Count(b.String(), "\n")
	available := m.height - consumed - footerLines
	if dp := renderDomainsPanel(m.snap.NetworkEvents, w, available, m.snap.FetchedAt); dp != "" {
		b.WriteByte('\n')
		b.WriteString(dp)
	}

	// Footer.
	b.WriteByte('\n')
	b.WriteString(footer)
	b.WriteByte('\n')

	return b.String()
}

// --- commands ---

func fullTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return fullTickMsg(t) })
}

// listenDaemonEvents waits for the next push notification from the daemon
// subscription channel. Returns nil when the channel is closed.
func listenDaemonEvents(ch <-chan daemon.SubscribeEvent) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return nil
		}
		return daemonEventMsg{event: evt}
	}
}

// fetchSkeleton returns a fast snapshot from local file reads so the TUI
// can render the dashboard layout immediately while heavy work continues.
func fetchSkeleton(mon *monitor.Monitor, gen uint64) tea.Cmd {
	return func() tea.Msg {
		return snapshotMsg{snap: mon.FetchSkeleton(), gen: gen, skeleton: true}
	}
}

// fetchHeavy runs discovery and daemon RPCs on top of a skeleton snapshot.
func fetchHeavy(mon *monitor.Monitor, base *monitor.Snapshot, gen uint64) tea.Cmd {
	return func() tea.Msg {
		return snapshotMsg{snap: mon.FetchHeavy(context.Background(), base), gen: gen}
	}
}

func fetchFull(mon *monitor.Monitor, gen uint64) tea.Cmd {
	return func() tea.Msg {
		return snapshotMsg{snap: mon.FetchFull(context.Background()), gen: gen}
	}
}

// --- render: header + status ---

func (m model) renderHeader(w int) string {
	title := titleStyle.Render("human tui")
	if host, err := os.Hostname(); err == nil && host != "" {
		title = titleStyle.Render("human tui") + subtleStyle.Render(" — "+host)
	}

	right := ""
	if m.snap != nil {
		ws := claude.WindowStart(m.snap.FetchedAt)
		we := claude.WindowEnd(ws)
		localStart := ws.Local()
		localEnd := we.Local()
		right = subtleStyle.Render(fmt.Sprintf("%02d:00 – %02d:00", localStart.Hour(), localEnd.Hour()))
	}

	gap := w - lipgloss.Width(title) - lipgloss.Width(right) - 4
	if gap < 1 {
		gap = 1
	}
	return "  " + title + strings.Repeat(" ", gap) + right
}

func renderStatusLine(snap *monitor.Snapshot, w int) string {
	var left string
	if snap.Daemon.Alive {
		left = "  " + specialStyle.Render("●") + " Daemon running"
		if snap.Daemon.ProxyAddr != "" {
			if snap.Daemon.ProxyActiveConns > 0 {
				left += "  " + specialStyle.Render(fmt.Sprintf("Proxy: %d active", snap.Daemon.ProxyActiveConns))
			} else {
				left += "  " + subtleStyle.Render("Proxy: idle")
			}
		}
	} else {
		left = "  " + accentStyle.Render("●") + " Daemon stopped"
	}

	var rightParts []string
	if snap.Telegram != "" {
		rightParts = append(rightParts, snap.Telegram)
	}
	if snap.Slack != "" {
		rightParts = append(rightParts, snap.Slack)
	}
	right := subtleStyle.Render(strings.Join(rightParts, "  "))
	gap := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// --- render: instances with progress bars ---

// renderInstancesAndPanes writes instance rows (or an empty placeholder)
// and tmux pane rows into the builder.
func (m model) renderInstancesAndPanes(b *strings.Builder, w int) {
	filtered := m.filterInstances(m.snap.Instances)
	if len(filtered) == 0 {
		if m.fetching {
			b.WriteString("  " + m.spinner.View() + subtleStyle.Render(" Discovering instances..."))
		} else {
			b.WriteString(subtleStyle.Render("  No active instances"))
		}
		b.WriteByte('\n')
	} else {
		for _, iv := range filtered {
			m.renderInstance(b, iv, w)
		}
		renderTotalLine(b, m.snap.TotalUsage, w)
	}

	if len(m.snap.Panes) > 0 {
		b.WriteByte('\n')
		b.WriteString(renderPanes(m.snap.Panes))
	}
}

func (m model) renderInstance(b *strings.Builder, iv monitor.InstanceView, w int) {
	b.WriteByte('\n')

	// Instance header: icon + label + elapsed + slug
	// Override with confirm state when this instance has a pending confirmation.
	var icon string
	var labelStyle lipgloss.Style
	if iv.Usage.Instance.PID > 0 && m.confirmPIDs[iv.Usage.Instance.PID] {
		icon = confirmStyle.Render("●")
		labelStyle = confirmStyle
	} else {
		icon = m.sessionIcon(iv.Session)
		labelStyle = sessionLabelStyle(iv.Session)
	}
	header := "  " + icon + " " + labelStyle.Render(iv.Usage.Instance.Label)
	if iv.Usage.Instance.DaemonConnected {
		if iv.Usage.Instance.ProxyConfigured {
			header += "  " + specialStyle.Render("⚡+proxy")
		} else {
			header += "  " + specialStyle.Render("⚡")
		}
	} else if iv.Usage.Instance.ProxyConfigured {
		header += "  " + specialStyle.Render("proxy")
	}
	if mem := claude.FormatMemory(iv.Usage.Instance.Memory); mem != "" {
		header += "  " + subtleStyle.Render(mem)
	}
	if iv.Session != nil && !iv.Session.StartedAt.IsZero() {
		header += "  " + subtleStyle.Render(formatElapsed(time.Since(iv.Session.StartedAt)))
	}
	if iv.Session != nil && iv.Session.Slug != "" {
		header += "  " + slugStyle.Render(iv.Session.Slug)
	}
	if ctx := sessionContext(iv.Session); ctx != "" {
		header += "  " + ctx
	}
	b.WriteString(header)
	b.WriteByte('\n')

	// Progress bars per model.
	renderModelBars(b, iv.Usage.Summary, w)

	// Subagents + tasks.
	if iv.Session != nil {
		m.renderSubagents(b, iv.Session.Subagents)
		renderTaskSummary(b, iv.Session.Tasks)
	}
}

func renderModelBars(b *strings.Builder, summary *claude.UsageSummary, w int) {
	if summary == nil {
		return
	}

	var grandTotal int
	for _, mu := range summary.Models {
		if mu != nil {
			grandTotal += mu.Total()
		}
	}
	if grandTotal == 0 {
		return
	}

	// Sort model names for stable output.
	models := make([]string, 0, len(summary.Models))
	for name, mu := range summary.Models {
		if mu != nil && mu.Total() > 0 {
			models = append(models, name)
		}
	}
	sort.Strings(models)

	// Bar width: total width - indent(4) - label(12) - stats(~30) - padding(4)
	barWidth := w - 50
	if barWidth < 10 {
		barWidth = 10
	}
	if barWidth > 50 {
		barWidth = 50
	}

	for _, name := range models {
		mu := summary.Models[name]
		if mu == nil {
			continue
		}
		pct := float64(mu.Total()) / float64(grandTotal)

		bar := progress.New(
			progress.WithSolidFill(modelColor(name)),
			progress.WithoutPercentage(),
		)
		bar.Width = barWidth

		stats := fmt.Sprintf("  %3.0f%%  %s in  %s out",
			pct*100, formatTokens(mu.InputTokens), formatTokens(mu.OutputTokens))

		_, _ = fmt.Fprintf(b, "    %-12s %s%s\n", name, bar.ViewAs(pct), subtleStyle.Render(stats))
	}
}

// --- render: subagents ---

func (m model) renderSubagents(b *strings.Builder, subagents []logparser.Subagent) {
	if len(subagents) == 0 {
		return
	}

	// Filter out completed agents older than 5s.
	var visible []logparser.Subagent
	for _, sa := range subagents {
		if sa.CompletedAt != nil && time.Since(*sa.CompletedAt) > 5*time.Second {
			continue
		}
		visible = append(visible, sa)
	}
	if len(visible) == 0 {
		return
	}

	start := 0
	if len(visible) > 5 {
		start = len(visible) - 5
	}
	for i := start; i < len(visible); i++ {
		sa := visible[i]
		agentType := sa.SubagentType
		if agentType == "" {
			agentType = "agent"
		}
		desc := truncate(sa.Description, 40)

		if sa.CompletedAt != nil {
			elapsed := formatAgentDuration(sa)
			_, _ = fmt.Fprintf(b, "      %s %s %s\n",
				subtleStyle.Render("✓"),
				subtleStyle.Render(desc),
				subtleStyle.Render(fmt.Sprintf("(%s, %s)", agentType, elapsed)))
		} else {
			elapsed := formatElapsed(time.Since(sa.StartedAt))
			_, _ = fmt.Fprintf(b, "      %s %s %s\n",
				m.spinner.View(),
				desc,
				subtleStyle.Render(fmt.Sprintf("(%s, %s)", agentType, elapsed)))
		}
	}
}

func formatAgentDuration(sa logparser.Subagent) string {
	if sa.DurationMs > 0 {
		return formatElapsed(time.Duration(sa.DurationMs) * time.Millisecond)
	}
	if sa.CompletedAt != nil {
		return formatElapsed(sa.CompletedAt.Sub(sa.StartedAt))
	}
	return "0s"
}

// --- render: tasks ---

func renderTaskSummary(b *strings.Builder, tasks []logparser.Task) {
	if len(tasks) == 0 {
		return
	}

	var pending, inProgress, completed int
	for _, t := range tasks {
		switch t.Status {
		case "completed":
			completed++
		case "in_progress":
			inProgress++
		default:
			pending++
		}
	}

	parts := []string{}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	if inProgress > 0 {
		parts = append(parts, fmt.Sprintf("%d in progress", inProgress))
	}
	if completed > 0 {
		parts = append(parts, fmt.Sprintf("%d done", completed))
	}

	_, _ = fmt.Fprintf(b, "      Tasks: %s\n", subtleStyle.Render(strings.Join(parts, " · ")))
}

// --- render: totals ---

func renderTotalLine(b *strings.Builder, total *claude.UsageSummary, w int) {
	b.WriteByte('\n')
	rule := ruleStyle.Render(strings.Repeat("─", w-4))
	b.WriteString("  " + rule + "\n")

	var parts []string
	for name, mu := range total.Models {
		if mu != nil && mu.Total() > 0 {
			parts = append(parts, fmt.Sprintf("%s: %s in · %s out",
				name, formatTokens(mu.InputTokens), formatTokens(mu.OutputTokens)))
		}
	}
	sort.Strings(parts)

	if len(parts) > 0 {
		b.WriteString("  " + subtleStyle.Render("Total  "+strings.Join(parts, "  ")) + "\n")
	}
}

// --- render: trackers ---

func renderTrackers(trackers []tracker.TrackerStatus, _ int) string {
	counts := make(map[string]int)
	labels := make(map[string]string) // kind → Label
	var order []string
	for _, t := range trackers {
		if !t.Working {
			continue
		}
		if counts[t.Kind] == 0 {
			order = append(order, t.Kind)
			labels[t.Kind] = t.Label
		}
		counts[t.Kind]++
	}
	if len(order) == 0 {
		return ""
	}

	var parts []string
	for _, kind := range order {
		s := labels[kind]
		if counts[kind] > 1 {
			s += fmt.Sprintf(" (%d)", counts[kind])
		}
		parts = append(parts, s)
	}

	return "  " + subtleStyle.Render("Trackers") + "  " + strings.Join(parts, "  ")
}

// --- render: tmux panes ---

func renderPanes(panes []claude.TmuxPane) string {
	var parts []string
	for _, p := range panes {
		var icon string
		switch p.State {
		case claude.StateBusy:
			icon = accentStyle.Render("●")
		case claude.StateReady:
			icon = specialStyle.Render("●")
		case claude.StateBlocked:
			icon = warningStyle.Render("●")
		case claude.StateWaiting:
			icon = waitingStyle.Render("●")
		case claude.StateError:
			icon = accentStyle.Render("⚠")
		case claude.StateConfirm:
			icon = confirmStyle.Render("●")
		default:
			icon = "○"
		}
		label := fmt.Sprintf("%q (%d:%d)", p.SessionName, p.WindowIndex, p.PaneIndex)
		if p.Devcontainer {
			label += " (devcontainer)"
		}
		parts = append(parts, icon+" "+label)
	}
	return "  " + subtleStyle.Render("Tmux") + "  " + strings.Join(parts, "   ")
}

// --- render: footer ---

func renderConfirmDialog(prompt string, width, height int) string {
	title := confirmStyle.Render("⚠ " + prompt)
	hints := subtleStyle.Render("y confirm  n abort")
	content := title + "\n\n" + hints
	dialog := confirmDialogStyle.Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, dialog)
}

func renderFooter(w int, logMode, dispatchStatus string, showTabs bool) string {
	left := subtleStyle.Render("  ↻ live")
	if logMode != "" {
		left += "  " + subtleStyle.Render("log:"+logMode)
	}
	if dispatchStatus != "" {
		left += "  " + specialStyle.Render(dispatchStatus)
	}
	keys := "j/k nav  ⏎ send  o open  n new  a agent  l log  q quit"
	if showTabs {
		keys = "Tab switch  " + keys
	}
	right := subtleStyle.Render(keys)
	gap := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// --- render: icon ---

func sessionLabelStyle(sess *logparser.SessionState) lipgloss.Style {
	if sess == nil {
		return idleInstanceStyle
	}
	switch sess.Status {
	case logparser.StatusWorking:
		return busyInstanceStyle
	case logparser.StatusError:
		return errorStyle
	case logparser.StatusBlocked:
		return warningStyle
	case logparser.StatusWaiting:
		return specialStyle
	default:
		return idleInstanceStyle
	}
}

// sessionContext returns a styled string with contextual info from hook events:
// current tool being executed, blocked tool name, or error type.
func sessionContext(sess *logparser.SessionState) string {
	if sess == nil {
		return ""
	}
	switch {
	case sess.Status == logparser.StatusError && sess.ErrorType != "":
		return errorStyle.Render(sess.ErrorType)
	case sess.Status == logparser.StatusBlocked && sess.BlockedTool != "":
		return warningStyle.Render("⚠ " + sess.BlockedTool)
	case sess.CurrentTool != "":
		return subtleStyle.Render("[" + sess.CurrentTool + "]")
	default:
		return ""
	}
}

func (m model) sessionIcon(sess *logparser.SessionState) string {
	if sess == nil {
		return subtleStyle.Render("○")
	}
	switch sess.Status {
	case logparser.StatusWorking:
		return m.spinner.View()
	case logparser.StatusError:
		return accentStyle.Render("⚠")
	case logparser.StatusBlocked:
		return warningStyle.Render("●")
	case logparser.StatusWaiting:
		return waitingStyle.Render("●")
	default:
		if !sess.LastActivity.IsZero() {
			return specialStyle.Render("●")
		}
		return subtleStyle.Render("○")
	}
}

// --- utilities ---

func formatElapsed(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Guard against panics for maxLen<3: the "..." suffix alone is
	// already that long, so just emit the suffix truncated to fit.
	if maxLen < 3 {
		if maxLen <= 0 {
			return ""
		}
		return "..."[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// formatTokens delegates to claude.FormatTokens for token count formatting.
func formatTokens(n int) string {
	return claude.FormatTokens(n)
}

// --- project tabs ---

// tab represents a single project tab in the TUI.
type tab struct {
	Name string // display name
	Dir  string // project directory (empty for "Other" tab)
}

// tabs builds the list of visible tabs from registered projects.
// Returns nil when no projects are registered.
func (m model) tabs() []tab {
	if len(m.projects) == 0 {
		return nil
	}
	out := make([]tab, 0, len(m.projects)+1)
	for _, p := range m.projects {
		out = append(out, tab{Name: p.Name, Dir: p.Dir})
	}
	// Append "Other" tab only if there are unmatched instances.
	if m.snap != nil && hasUnmatchedInstances(m.snap.Instances, m.projects) {
		out = append(out, tab{Name: "Other"})
	}
	return out
}

// filterInstances returns the instances that belong to the active tab.
// When there are no tabs (single project or none), all instances are returned.
func (m model) filterInstances(instances []monitor.InstanceView) []monitor.InstanceView {
	tabs := m.tabs()
	if len(tabs) == 0 {
		return instances
	}
	// An out-of-range activeTab previously returned the full list, so
	// a stale index silently widened the filter. Return empty so the
	// caller observes a consistent "no tab is active" result.
	if m.activeTab < 0 || m.activeTab >= len(tabs) {
		return nil
	}
	active := tabs[m.activeTab]
	if active.Dir == "" {
		// "Other" tab: instances not matching any project.
		return unmatchedInstances(instances, m.projects)
	}
	var out []monitor.InstanceView
	for _, iv := range instances {
		if pathMatches(iv.Usage.Instance.Cwd, active.Dir) {
			out = append(out, iv)
		}
	}
	return out
}

// hasUnmatchedInstances returns true if any instance does not match a registered project.
func hasUnmatchedInstances(instances []monitor.InstanceView, projects []daemon.ProjectInfo) bool {
	for _, iv := range instances {
		if !matchesAnyProject(iv.Usage.Instance.Cwd, projects) {
			return true
		}
	}
	return false
}

// unmatchedInstances returns instances whose Cwd does not match any project dir.
func unmatchedInstances(instances []monitor.InstanceView, projects []daemon.ProjectInfo) []monitor.InstanceView {
	var out []monitor.InstanceView
	for _, iv := range instances {
		if !matchesAnyProject(iv.Usage.Instance.Cwd, projects) {
			out = append(out, iv)
		}
	}
	return out
}

// filterPanes returns the panes that belong to the active tab.
// When there are no tabs (single project or none), all panes are returned.
func (m model) filterPanes(panes []claude.TmuxPane) []claude.TmuxPane {
	tabs := m.tabs()
	if len(tabs) == 0 {
		return panes
	}
	// Symmetric to filterInstances: reject stale activeTab values
	// rather than widening the filter to include everything.
	if m.activeTab < 0 || m.activeTab >= len(tabs) {
		return nil
	}
	active := tabs[m.activeTab]
	if active.Dir == "" {
		return unmatchedPanes(panes, m.projects)
	}
	var out []claude.TmuxPane
	for _, p := range panes {
		if pathMatches(p.Cwd, active.Dir) {
			out = append(out, p)
		}
	}
	return out
}

// unmatchedPanes returns panes whose Cwd does not match any project dir.
func unmatchedPanes(panes []claude.TmuxPane, projects []daemon.ProjectInfo) []claude.TmuxPane {
	var out []claude.TmuxPane
	for _, p := range panes {
		if !matchesAnyProject(p.Cwd, projects) {
			out = append(out, p)
		}
	}
	return out
}

// pathMatches returns true when cwd is exactly dir or a proper
// descendant path under dir. Using a bare HasPrefix wrongly treats
// "/home/alice-project" as a descendant of "/home/alice" because the
// prefix happens to match at a non-boundary byte.
func pathMatches(cwd, dir string) bool {
	if dir == "" {
		return false
	}
	return cwd == dir || strings.HasPrefix(cwd, dir+string(os.PathSeparator))
}

// matchesAnyProject returns true if cwd is dir or a descendant path of
// any project's Dir.
func matchesAnyProject(cwd string, projects []daemon.ProjectInfo) bool {
	for _, p := range projects {
		if pathMatches(cwd, p.Dir) {
			return true
		}
	}
	return false
}

// renderTabBar renders a horizontal tab bar. Returns "" when tabs are not applicable.
func renderTabBar(tabs []tab, active int, w int) string {
	if len(tabs) == 0 {
		return ""
	}

	var parts []string
	for i, t := range tabs {
		label := fmt.Sprintf(" %d:%s ", i+1, t.Name)
		if i == active {
			parts = append(parts, activeTabStyle.Render(label))
		} else {
			parts = append(parts, inactiveTabStyle.Render(label))
		}
	}
	line := "  " + strings.Join(parts, " ")
	// Pad or truncate to width.
	visible := lipgloss.Width(line)
	if visible < w-2 {
		line += strings.Repeat(" ", w-2-visible)
	}
	return line
}

// fetchProjectsCmd loads project info from the daemon info file.
func fetchProjectsCmd() tea.Cmd {
	return func() tea.Msg {
		info, err := daemon.ReadInfo()
		if err != nil {
			return projectsMsg(nil)
		}
		return projectsMsg(info.Projects)
	}
}

// --- log mode ---

// cycleLogMode cycles through full → meta → off → full.
func cycleLogMode(current string) string {
	switch current {
	case "full":
		return "meta"
	case "meta":
		return "off"
	default:
		return "full"
	}
}

// daemonAddr returns the daemon address and token for direct TCP communication.
func daemonAddr() (string, string) {
	addr := os.Getenv("HUMAN_DAEMON_ADDR")
	token := os.Getenv("HUMAN_DAEMON_TOKEN")
	if addr == "" {
		if info, err := daemon.ReadInfo(); err == nil {
			addr = info.Addr
			if token == "" {
				token = info.Token
			}
		}
	}
	return addr, token
}

// fetchLogModeCmd fetches the current log mode from the daemon.
func fetchLogModeCmd() tea.Cmd {
	return func() tea.Msg {
		addr, token := daemonAddr()
		if addr == "" {
			return logModeMsg("full")
		}
		mode, err := daemon.GetLogMode(addr, token)
		if err != nil {
			return logModeMsg("full")
		}
		return logModeMsg(mode)
	}
}

// setLogModeCmd sends a log-mode change to the daemon.
func setLogModeCmd(mode string) tea.Cmd {
	return func() tea.Msg {
		addr, token := daemonAddr()
		if addr == "" {
			return logModeMsg(mode)
		}
		result, err := daemon.SetLogMode(addr, token, mode)
		if err != nil {
			return logModeMsg(mode) // optimistic
		}
		return logModeMsg(result)
	}
}

// --- issue fetching ---

func issueTickCmd() tea.Cmd {
	return tea.Tick(30*time.Second, func(t time.Time) tea.Msg { return issueTickMsg(t) })
}

func fetchIssuesCmd() tea.Cmd {
	return func() tea.Msg {
		addr, token := daemonAddr()
		if addr == "" {
			return issuesResultMsg{}
		}
		results, err := daemon.GetTrackerIssues(addr, token)
		if err != nil {
			return issuesResultMsg{}
		}
		return issuesResultMsg{results: fromDaemonResults(results)}
	}
}

func fromDaemonResults(results []daemon.TrackerIssuesResult) []trackerIssues {
	out := make([]trackerIssues, len(results))
	for i, r := range results {
		out[i] = trackerIssues{
			TrackerName: r.TrackerName,
			TrackerKind: r.TrackerKind,
			TrackerRole: r.TrackerRole,
			Project:     r.Project,
			Issues:      r.Issues,
		}
		if len(r.ReadyForReview) > 0 {
			set := make(map[string]bool, len(r.ReadyForReview))
			for _, k := range r.ReadyForReview {
				set[k] = true
			}
			out[i].ReadyForReview = set
		}
		if len(r.ReadyForReviewPRs) > 0 {
			out[i].ReadyForReviewPRs = r.ReadyForReviewPRs
		}
		if r.Err != "" {
			out[i].Err = fmt.Errorf("%s", r.Err)
		}
	}
	return out
}

// --- render: issues ---

// inferRole returns a role based on tracker kind when no explicit role is configured.
func inferRole(trackerKind string) string {
	switch trackerKind {
	case "shortcut":
		return "pm"
	case "linear":
		return "engineering"
	default:
		return ""
	}
}

// pipelineStage maps a tracker role and status type to a human-readable
// pipeline stage label. The pipeline model is:
//
//	PM:   Ready for Plan -> Planning -> Planned
//	Eng:  Backlog -> In Dev -> Done -> Closed
func pipelineStage(trackerKind, trackerRole, statusName string, statusType tracker.Category) string {
	role := trackerRole
	if role == "" {
		role = inferRole(trackerKind)
	}
	switch role {
	case "pm":
		switch statusType {
		case tracker.CategoryUnstarted:
			return "Ready for Plan"
		case tracker.CategoryStarted:
			return "Planning"
		case tracker.CategoryDone:
			return "Planned"
		default:
			return statusName
		}
	case "engineering":
		switch statusType {
		case tracker.CategoryUnstarted:
			return "Backlog"
		case tracker.CategoryStarted:
			return "In Dev"
		case tracker.CategoryDone:
			return "Done"
		case tracker.CategoryClosed:
			return "Closed"
		default:
			return statusName
		}
	default:
		return statusName
	}
}

// pipelineStageStyle returns a lipgloss style for the given category,
// reflecting progress through the pipeline.
func pipelineStageStyle(statusType tracker.Category) lipgloss.Style {
	switch statusType {
	case tracker.CategoryStarted:
		return warningStyle // yellow -- in progress
	case tracker.CategoryDone:
		return specialStyle // teal -- complete
	case tracker.CategoryUnstarted, tracker.CategoryClosed:
		return subtleStyle
	default:
		return subtleStyle
	}
}

// pipelineName returns a display label based on the tracker's role.
func pipelineName(trackerKind, trackerRole string) string {
	role := trackerRole
	if role == "" {
		role = inferRole(trackerKind)
	}
	switch role {
	case "pm":
		return warningStyle.Render("PM")
	case "engineering":
		return specialStyle.Render("Eng")
	default:
		return subtleStyle.Render(trackerKind)
	}
}

func renderIssuesPanel(groups []trackerIssues, fetchedAt time.Time, w, cursor int) string {
	if len(groups) == 0 {
		return ""
	}

	var b strings.Builder

	header := "  " + subtleStyle.Render("Pipeline")
	if !fetchedAt.IsZero() {
		header += "  " + subtleStyle.Render(formatElapsed(time.Since(fetchedAt))+" ago")
	}
	b.WriteString(header)
	b.WriteByte('\n')

	flatIdx := 0
	first := true
	for _, g := range groups {
		if g.Err != nil {
			if !first {
				b.WriteByte('\n')
			}
			first = false
			_, _ = fmt.Fprintf(&b, "    %s %s/%s: %s\n",
				errorStyle.Render("!"),
				g.TrackerKind, g.Project,
				subtleStyle.Render("fetch failed"))
			continue
		}
		if len(g.Issues) == 0 {
			continue
		}

		if !first {
			b.WriteByte('\n')
		}
		first = false

		pipelineLabel := pipelineName(g.TrackerKind, g.TrackerRole)
		_, _ = fmt.Fprintf(&b, "    %s %s %s\n",
			subtleStyle.Render("▸"),
			pipelineLabel,
			subtleStyle.Render(g.Project))

		for _, issue := range g.Issues {
			title := truncate(issue.Title, w-38)
			stage := pipelineStage(g.TrackerKind, g.TrackerRole, issue.Status, issue.StatusType)
			stageStyled := pipelineStageStyle(issue.StatusType).Render(truncate(stage, 14))
			keyStyle := titleStyle
			prefix := "      "
			if flatIdx == cursor {
				keyStyle = selectedStyle
				prefix = "    ▸ "
			}
			// (B) marks defect tickets so the eye can spot bugs without
			// reading types; (R) marks engineering tickets currently flagged
			// ready for review on their PM ticket. The two markers are
			// independent — a ticket may carry both.
			bugMarker := "   "
			if issue.IsBug() {
				bugMarker = errorStyle.Render("(B)")
			}
			_, _ = fmt.Fprintf(&b, "%s%-12s %s %s %-14s %s\n",
				prefix,
				keyStyle.Render(issue.Key),
				bugMarker,
				reviewMarker(g.ReadyForReview[issue.Key], g.ReadyForReviewPRs[issue.Key]),
				stageStyled,
				title)
			flatIdx++
		}
	}

	return b.String()
}

// renderDomainsPanel renders the ambient network activity panel at the
// bottom of the TUI. It shows up to `available` rows, newest on top,
// with consecutive-host dedup counts and a small source tag.
//
// available is the vertical budget in rows. When available <= 1 the
// reviewMarker renders the (R) ready-for-review annotation. When a pull-request
// URL is known (from the handoff's pr: line), the marker is wrapped in an OSC 8
// hyperlink so it opens the PR in terminals that support clickable links;
// terminals that don't simply show "(R)". Returns blank spacing when the issue
// is not flagged for review.
func reviewMarker(ready bool, prURL string) string {
	if !ready {
		return "   "
	}
	marker := specialStyle.Render("(R)")
	if prURL == "" {
		return marker
	}
	return "\x1b]8;;" + prURL + "\x1b\\" + marker + "\x1b]8;;\x1b\\"
}

// panel renders nothing so the footer cannot be clipped on very short
// terminals. Zero events also collapses to nothing rather than leaving
// a visible empty frame.
func renderDomainsPanel(events []daemon.NetworkEvent, w, available int, now time.Time) string {
	if available <= 1 || len(events) == 0 {
		return ""
	}

	// Reserve one row for the panel header.
	headerRow := "  " + subtleStyle.Render("Network")
	rowBudget := available - 1
	if rowBudget < 1 {
		return ""
	}

	// Snapshot is oldest-first. Reverse to newest-on-top so the rows
	// closest to the footer are the oldest ones the user is least
	// likely to miss.
	reversed := make([]daemon.NetworkEvent, len(events))
	for i, e := range events {
		reversed[len(events)-1-i] = e
	}
	if len(reversed) > rowBudget {
		reversed = reversed[:rowBudget]
	}

	var b strings.Builder
	b.WriteString(headerRow)
	b.WriteByte('\n')

	for i, evt := range reversed {
		b.WriteString("    ")
		b.WriteString(renderDomainRow(evt, w, now))
		if i < len(reversed)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// renderDomainRow formats a single network event row as
//
//	<source-tag>  <host[ xN]>   <relative time>
//
// The source tag colour encodes the status so block/fail read pink,
// intercept reads gold, and forward/oauth read teal.
func renderDomainRow(evt daemon.NetworkEvent, w int, now time.Time) string {
	tagStyle := domainSourceStyle(evt.Source, evt.Status)
	tag := tagStyle.Render(fmt.Sprintf("[%s]", evt.Source))

	host := evt.Host
	if host == "" {
		host = "(no sni)"
	}
	if evt.Count > 1 {
		host = fmt.Sprintf("%s x%d", host, evt.Count)
	}
	// Truncate the host to prevent overflow on narrow terminals. The
	// lower bound of 10 keeps at least enough room for a short hostname
	// plus the count suffix.
	hostMax := w - 24
	if hostMax < 10 {
		hostMax = 10
	}
	host = truncate(host, hostMax)

	rel := subtleStyle.Render(formatElapsed(now.Sub(evt.LastSeen)) + " ago")
	return fmt.Sprintf("%s  %-*s  %s", tag, hostMax, host, rel)
}

// domainSourceStyle picks a colour based on source + status.
// Blocks and failures are pink; intercepts are gold; forwards and
// oauth callbacks are teal. Anything else falls back to subtle.
func domainSourceStyle(source, status string) lipgloss.Style {
	switch source {
	case "fail":
		return errorStyle
	case "oauth":
		return specialStyle
	case "proxy":
		switch status {
		case "block":
			return errorStyle
		case "intercept":
			return warningStyle
		case "forward":
			return specialStyle
		}
	}
	return subtleStyle
}

// sparkline renders a Unicode sparkline from values, scaled to fit width
// characters. Each output character maps to one value. When len(values)
// exceeds width, values are down-sampled by averaging buckets; when
// len(values) is less than width, one character per value is used (no
// stretching). Returns an empty string when values is empty or width < 1.
func sparkline(values []int, width int) string {
	if len(values) == 0 || width < 1 {
		return ""
	}

	blocks := []rune("▁▂▃▄▅▆▇█")
	levels := len(blocks) // 8

	// Down-sample if we have more values than width.
	display := values
	if len(values) > width {
		display = make([]int, width)
		for i := range display {
			start := i * len(values) / width
			end := (i + 1) * len(values) / width
			if end > len(values) {
				end = len(values)
			}
			sum := 0
			for _, v := range values[start:end] {
				sum += v
			}
			count := end - start
			if count > 0 {
				display[i] = sum / count
			}
		}
	}

	// Find max for scaling.
	maxVal := 0
	for _, v := range display {
		if v > maxVal {
			maxVal = v
		}
	}

	var sb strings.Builder
	sb.Grow(len(display) * 4) // UTF-8 block chars are up to 3 bytes
	for _, v := range display {
		if maxVal == 0 {
			sb.WriteRune(blocks[0])
			continue
		}
		idx := (v * (levels - 1)) / maxVal
		if idx >= levels {
			idx = levels - 1
		}
		sb.WriteRune(blocks[idx])
	}
	return sb.String()
}

// byHourToValues expands sparse TimeBucket data into a dense slice
// covering the window from since to until. Hours not present in
// buckets are filled with zero. The bucket format is "2006-01-02 15:00".
func byHourToValues(buckets []stats.TimeBucket, since, until time.Time) []int {
	// Compute the number of hour slots in the window.
	sinceHour := since.UTC().Truncate(time.Hour)
	untilHour := until.UTC().Truncate(time.Hour)
	hours := int(untilHour.Sub(sinceHour)/time.Hour) + 1
	if hours < 1 {
		hours = 1
	}
	if hours > 168 { // safety cap at 1 week
		hours = 168
	}

	values := make([]int, hours)

	// Build a lookup from bucket label to count.
	lookup := make(map[string]int, len(buckets))
	for _, b := range buckets {
		lookup[b.Bucket] = b.Count
	}

	// Fill the dense array.
	for i := 0; i < hours; i++ {
		label := sinceHour.Add(time.Duration(i) * time.Hour).Format("2006-01-02 15:00")
		values[i] = lookup[label]
	}

	return values
}

// renderToolStatsPanel renders the historical tool call statistics panel.
// It shows tool call distribution from the pre-aggregated ToolStats in
// the snapshot. Returns empty string when there are no events.
func renderToolStatsPanel(ts *stats.ToolStats, w int) string {
	if ts == nil || ts.TotalEvents == 0 {
		return ""
	}

	var b strings.Builder
	header := fmt.Sprintf("  %s  %s  %s",
		subtleStyle.Render("Tools (24h)"),
		titleStyle.Render(fmt.Sprintf("%d", ts.TotalEvents)),
		subtleStyle.Render("events"))
	b.WriteString(header)
	b.WriteByte('\n')

	// Sparkline from hourly data.
	if len(ts.ByHour) > 0 {
		values := byHourToValues(ts.ByHour, ts.Since, ts.Until)
		sparkWidth := w - 6 // 4-char indent + 2-char margin
		if sparkWidth < 10 {
			sparkWidth = 10
		}
		if line := sparkline(values, sparkWidth); line != "" {
			_, _ = fmt.Fprintf(&b, "    %s\n", subtleStyle.Render(line))
		}
	}

	renderToolDistribution(&b, ts, w)
	renderOutcomes(&b, ts.ByEventName)

	return b.String()
}

func renderToolDistribution(b *strings.Builder, ts *stats.ToolStats, w int) {
	maxTools := 8
	if len(ts.ByTool) < maxTools {
		maxTools = len(ts.ByTool)
	}

	maxCount := 0
	for _, tc := range ts.ByTool[:maxTools] {
		if tc.Count > maxCount {
			maxCount = tc.Count
		}
	}

	barWidth := w - 40
	if barWidth < 10 {
		barWidth = 10
	}

	for _, tc := range ts.ByTool[:maxTools] {
		barLen := 0
		if maxCount > 0 {
			barLen = (tc.Count * barWidth) / maxCount
			if barLen < 1 && tc.Count > 0 {
				barLen = 1
			}
		}
		bar := strings.Repeat("=", barLen)
		pct := 0
		if ts.TotalEvents > 0 {
			pct = (tc.Count * 100) / ts.TotalEvents
		}
		_, _ = fmt.Fprintf(b, "    %-10s %s %d  (%d%%)\n",
			titleStyle.Render(tc.ToolName),
			specialStyle.Render(bar),
			tc.Count,
			pct)
	}
}

func renderOutcomes(b *strings.Builder, byEventName []stats.EventNameCount) {
	var successes, failures int
	for _, enc := range byEventName {
		switch enc.EventName {
		case "PostToolUse":
			successes += enc.Count
		case "PostToolUseFailure":
			failures += enc.Count
		}
	}
	if successes+failures > 0 {
		total := successes + failures
		errorRate := float64(failures) * 100.0 / float64(total)
		_, _ = fmt.Fprintf(b, "    %s %d ok, %d failed (%.1f%% error rate)",
			subtleStyle.Render("Outcomes:"),
			successes, failures, errorRate)
	}
}
