package monitor

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/claude/logparser"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/messaging/slack"
	"github.com/gethuman-sh/human/internal/messaging/telegram"
	"github.com/gethuman-sh/human/internal/proxy"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
)

// Monitor owns the data-fetching and state-reconciliation cycle for the TUI.
type Monitor struct {
	finder       claude.InstanceFinder
	dockerClient claude.DockerClient

	// parsersMu guards parsers. Although the TUI currently serialises
	// FetchFull via its own m.fetching flag, future callers
	// (background health checks, parallel tests) must not rely on that
	// invariant — concurrent map access on parsers would otherwise crash
	// the program.
	parsersMu sync.Mutex
	parsers   map[string]*logparser.FileParser
}

// New creates a Monitor. dockerClient may be nil when Docker is unavailable.
func New(finder claude.InstanceFinder, dc claude.DockerClient) *Monitor {
	return &Monitor{
		finder:       finder,
		dockerClient: dc,
		parsers:      make(map[string]*logparser.FileParser),
	}
}

// FetchSkeleton returns a minimal snapshot from cheap local file reads.
// The TUI renders immediately from this so the user sees the dashboard
// layout (daemon status, status line) while heavier work runs in the
// background via FetchHeavy.
func (m *Monitor) FetchSkeleton() *Snapshot {
	snap := &Snapshot{FetchedAt: time.Now()}
	pid, alive := daemon.ReadAlivePid()
	snap.Daemon = DaemonState{PID: pid, Alive: alive}
	if info, err := daemon.ReadInfo(); err == nil {
		snap.Daemon.ProxyAddr = info.ProxyAddr
	}
	snap.Daemon.ProxyActiveConns = proxy.ReadStats(proxy.StatsPath()).ActiveConns
	snap.Telegram = telegramStatus()
	snap.Slack = slackStatus()
	return snap
}

// FetchHeavy performs discovery, daemon RPCs, and JSONL parsing on top of
// a skeleton snapshot. Independent daemon RPCs run in parallel with instance
// discovery so the total time is max(discovery, RPCs) rather than a sum.
func (m *Monitor) FetchHeavy(ctx context.Context, base *Snapshot) *Snapshot {
	snap := *base
	snap.FetchedAt = time.Now()

	// Read daemon info once for all RPC calls.
	var addr, token string
	if snap.Daemon.Alive {
		if info, err := daemon.ReadInfo(); err == nil {
			addr, token = info.Addr, info.Token
		}
	}

	// Launch independent daemon RPCs in parallel with discovery.
	var wg sync.WaitGroup
	var (
		trackers  []tracker.TrackerStatus
		hookSnaps map[string]hookevents.SessionSnapshot
		netEvents []daemon.NetworkEvent
		toolStats *stats.ToolStats
	)
	if addr != "" {
		wg.Add(4)
		go func() { defer wg.Done(); trackers, _ = daemon.GetTrackerDiagnose(addr, token) }()
		go func() { defer wg.Done(); hookSnaps, _ = daemon.GetHookSnapshot(addr, token) }()
		go func() { defer wg.Done(); netEvents, _ = daemon.GetNetworkEvents(addr, token) }()
		go func() { defer wg.Done(); toolStats, _ = daemon.GetToolStats(addr, token) }()
	}

	// Discovery runs in parallel with the daemon RPCs above.
	instances, err := m.finder.FindInstances(ctx)
	if err != nil {
		snap.Err = err
		wg.Wait()
		return &snap
	}

	wg.Wait()

	snap.Trackers = trackers
	snap.NetworkEvents = netEvents
	snap.ToolStats = toolStats
	snap.connectedPIDs = readConnectedPIDs()

	// Sequential: depends on instances.
	usages := claude.CollectInstanceUsage(instances, snap.FetchedAt)
	panes := m.findPanes(ctx, instances)
	sessionByPath := m.parseSessions(instances)

	overlayHookState(sessionByPath, hookSnaps)
	fillMissingFromHooks(instances, sessionByPath, hookSnaps)

	snap.Instances = matchInstances(usages, sessionByPath)
	applyDaemonConnectedViews(snap.Instances, snap.connectedPIDs)
	matchPaneStates(panes, sessionByPath, instances)
	snap.Panes = panes
	snap.sessionByPath = sessionByPath
	snap.TotalUsage = aggregateUsage(usages)

	return &snap
}

// FetchFull performs complete discovery, JSONL parsing, and hook event reading.
// Used for periodic full refreshes after the initial skeleton+heavy load.
func (m *Monitor) FetchFull(ctx context.Context) *Snapshot {
	return m.FetchHeavy(ctx, m.FetchSkeleton())
}

// --- internal helpers ---

func (m *Monitor) findPanes(ctx context.Context, instances []claude.Instance) []claude.TmuxPane {
	containerIDs := collectContainerIDs(instances)
	runner := claude.OSCommandRunner{}
	tmuxClient := &claude.OSTmuxClient{Runner: runner}
	procLister := &claude.OSProcessLister{Runner: runner}
	panes, _ := claude.FindClaudePanes(ctx, tmuxClient, procLister, containerIDs)
	return panes
}

func (m *Monitor) parseSessions(instances []claude.Instance) map[string]logparser.SessionState {
	byPath := make(map[string]logparser.SessionState)
	reader := logparser.OSFileReader{}
	active := make(map[string]bool, len(instances))
	m.parsersMu.Lock()
	defer m.parsersMu.Unlock()
	for _, inst := range instances {
		if inst.FilePath == "" {
			continue
		}
		active[inst.FilePath] = true
		parser, ok := m.parsers[inst.FilePath]
		if !ok {
			parser = logparser.NewFileParser()
			m.parsers[inst.FilePath] = parser
		}
		state, parseErr := parser.Update(reader, inst.FilePath)
		if parseErr != nil {
			log.Debug().Err(parseErr).Str("path", inst.FilePath).Msg("session parse failed")
			continue
		}
		if state.SessionID != "" {
			byPath[inst.FilePath] = state
		}
	}
	m.parseContainerSessions(instances, byPath, active)

	// Prune parsers for files no longer referenced by any instance.
	// This prevents stale state from lingering when a PID's JSONL path
	// changes (e.g. after resolveJSONLPath corrects a startup race).
	for path := range m.parsers {
		if !active[path] {
			delete(m.parsers, path)
		}
	}
	return byPath
}

// parseContainerSessions parses JSONL from container instances via their
// in-memory data. Containers have no FilePath, so we key by Root.
// Must be called with parsersMu held.
func (m *Monitor) parseContainerSessions(instances []claude.Instance, byPath map[string]logparser.SessionState, active map[string]bool) {
	for _, inst := range instances {
		if inst.Source != "container" || inst.Root == "" {
			continue
		}
		active[inst.Root] = true
		bw, ok := inst.Walker.(*claude.ByteWalker)
		if !ok || len(bw.Data) == 0 {
			continue
		}
		parser, ok := m.parsers[inst.Root]
		if !ok {
			parser = logparser.NewFileParser()
			m.parsers[inst.Root] = parser
		}
		state, parseErr := parser.UpdateBytes(bw.Data)
		if parseErr != nil {
			log.Debug().Err(parseErr).Str("root", inst.Root).Msg("container session parse failed")
			continue
		}
		if state.SessionID != "" {
			byPath[inst.Root] = state
		}
	}
}

func collectContainerIDs(instances []claude.Instance) []string {
	var ids []string
	for _, inst := range instances {
		if inst.Source == "container" && inst.ContainerID != "" {
			ids = append(ids, inst.ContainerID)
		}
	}
	return ids
}

// overlayHookState updates sessions in byPath from hook snapshots.
// Hook state is authoritative only when its last event is at least as recent
// as the JSONL-derived last activity. Async hooks can arrive out of order
// (e.g. PermissionRequest after Stop), so stale hook snapshots are skipped.
func overlayHookState(byPath map[string]logparser.SessionState, hooks map[string]hookevents.SessionSnapshot) {
	if len(hooks) == 0 {
		return
	}
	for path, sess := range byPath {
		snap, ok := hooks[sess.SessionID]
		if !ok {
			continue
		}
		if snap.LastEventAt.Before(sess.LastActivity) {
			continue
		}
		sess.Status = snap.Status
		sess.CurrentTool = snap.CurrentTool
		sess.BlockedTool = snap.BlockedTool
		sess.ErrorType = snap.ErrorType
		sess.LastActivity = snap.LastEventAt
		byPath[path] = sess
	}
}

// fillMissingFromHooks creates session state from hook snapshots for instances
// that have no JSONL session yet (e.g. freshly started Claude waiting for input).
func fillMissingFromHooks(instances []claude.Instance, byPath map[string]logparser.SessionState, hooks map[string]hookevents.SessionSnapshot) {
	if len(hooks) == 0 {
		return
	}
	// Collect session IDs already matched via JSONL.
	matched := make(map[string]bool, len(byPath))
	for _, sess := range byPath {
		matched[sess.SessionID] = true
	}
	// Index unmatched hooks by cwd for instance matching. When
	// multiple snapshots share the same cwd, keep the one with the
	// most recent LastEventAt so ordering is deterministic across
	// runs (map iteration is random).
	byCwd := make(map[string]hookevents.SessionSnapshot)
	for _, snap := range hooks {
		if matched[snap.SessionID] || snap.Cwd == "" {
			continue
		}
		if existing, ok := byCwd[snap.Cwd]; ok {
			if !snap.LastEventAt.After(existing.LastEventAt) {
				continue
			}
		}
		byCwd[snap.Cwd] = snap
	}
	// Match instances without a session to hook snapshots by cwd.
	for _, inst := range instances {
		key := inst.FilePath
		if key == "" {
			key = inst.Root
		}
		if key == "" || inst.Cwd == "" {
			continue
		}
		if _, hasSession := byPath[key]; hasSession {
			continue
		}
		snap, ok := byCwd[inst.Cwd]
		if !ok {
			continue
		}
		byPath[key] = logparser.SessionState{
			SessionID:    snap.SessionID,
			Cwd:          snap.Cwd,
			Status:       snap.Status,
			LastActivity: snap.LastEventAt,
			CurrentTool:  snap.CurrentTool,
			BlockedTool:  snap.BlockedTool,
			ErrorType:    snap.ErrorType,
		}
	}
}

// matchInstances pairs each InstanceUsage with its matched session.
func matchInstances(usages []claude.InstanceUsage, byPath map[string]logparser.SessionState) []InstanceView {
	views := make([]InstanceView, len(usages))
	for i, iu := range usages {
		views[i] = InstanceView{Usage: iu}
		key := iu.Instance.FilePath
		if key == "" {
			key = iu.Instance.Root
		}
		if sess, ok := byPath[key]; ok {
			s := sess // copy
			views[i].Session = &s
		}
	}
	return views
}

// extractUsages returns the InstanceUsage slice from InstanceViews.
func extractUsages(views []InstanceView) []claude.InstanceUsage {
	usages := make([]claude.InstanceUsage, len(views))
	for i, v := range views {
		usages[i] = v.Usage
	}
	return usages
}

// extractInstances returns the Instance slice from InstanceViews.
func extractInstances(views []InstanceView) []claude.Instance {
	instances := make([]claude.Instance, len(views))
	for i, v := range views {
		instances[i] = v.Usage.Instance
	}
	return instances
}

// matchPaneStates sets each pane's State by matching to a parsed session.
func matchPaneStates(panes []claude.TmuxPane, byPath map[string]logparser.SessionState, instances []claude.Instance) {
	// Build PID → session map.
	byPID := make(map[int]logparser.SessionState)
	for _, inst := range instances {
		if inst.PID > 0 && inst.FilePath != "" {
			if s, ok := byPath[inst.FilePath]; ok {
				byPID[inst.PID] = s
			}
		}
	}

	sessions := make([]logparser.SessionState, 0, len(byPath))
	for _, s := range byPath {
		sessions = append(sessions, s)
	}

	for i := range panes {
		if panes[i].ClaudePID > 0 {
			if s, ok := byPID[panes[i].ClaudePID]; ok {
				panes[i].State = sessionToState(s)
				continue
			}
		}
		for _, s := range sessions {
			if s.Cwd != "" && panes[i].Cwd == s.Cwd {
				panes[i].State = sessionToState(s)
				break
			}
		}
	}
}

func sessionToState(s logparser.SessionState) claude.InstanceState {
	switch s.Status {
	case logparser.StatusWorking:
		return claude.StateBusy
	case logparser.StatusError:
		return claude.StateError
	case logparser.StatusBlocked:
		return claude.StateBlocked
	case logparser.StatusWaiting:
		return claude.StateWaiting
	default:
		return claude.StateReady
	}
}

func aggregateUsage(usages []claude.InstanceUsage) *claude.UsageSummary {
	total := &claude.UsageSummary{Models: make(map[string]*claude.ModelUsage)}
	for _, iu := range usages {
		claude.MergeUsage(total, iu.Summary)
	}
	return total
}

func telegramStatus() string {
	configs, err := telegram.LoadConfigs(".")
	if err != nil {
		return "Telegram: config error"
	}
	if len(configs) == 0 {
		return "Telegram: not configured"
	}
	instances, err := telegram.LoadInstances(".")
	if err != nil {
		return "Telegram: config error"
	}
	if len(instances) == 0 {
		return "Telegram: missing token"
	}
	return "Telegram dispatch"
}

func slackStatus() string {
	configs, err := slack.LoadConfigs(".")
	if err != nil {
		return "Slack: config error"
	}
	if len(configs) == 0 {
		return ""
	}
	instances, err := slack.LoadInstances(".")
	if err != nil {
		return "Slack: config error"
	}
	if len(instances) == 0 {
		return "Slack: missing token"
	}
	return "Slack connected"
}

// readConnectedPIDs reads the set of daemon-connected PIDs from disk.
func readConnectedPIDs() map[int]bool {
	pids := daemon.ReadConnected(daemon.ConnectedPath())
	if len(pids) == 0 {
		return nil
	}
	m := make(map[int]bool, len(pids))
	for _, pid := range pids {
		m[pid] = true
	}
	return m
}

// applyDaemonConnectedViews sets DaemonConnected on instance views whose PID is in the connected set.
func applyDaemonConnectedViews(views []InstanceView, connected map[int]bool) {
	for i := range views {
		pid := views[i].Usage.Instance.PID
		views[i].Usage.Instance.DaemonConnected = pid > 0 && connected[pid]
	}
}
