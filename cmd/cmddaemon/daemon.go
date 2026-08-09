package cmddaemon

import (
	"context"
	"encoding/json"
	goerrors "errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/audit"
	"github.com/gethuman-sh/human/internal/board"
	"github.com/gethuman-sh/human/internal/boardcache"
	"github.com/gethuman-sh/human/internal/botidentity"
	"github.com/gethuman-sh/human/internal/chrome"
	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/codenav"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/costledger"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/devcontainer"
	"github.com/gethuman-sh/human/internal/dispatch"
	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/gitrepo"
	"github.com/gethuman-sh/human/internal/logrotate"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/messaging/slack"
	"github.com/gethuman-sh/human/internal/messaging/telegram"
	"github.com/gethuman-sh/human/internal/mockups"
	"github.com/gethuman-sh/human/internal/proxy"
	"github.com/gethuman-sh/human/internal/recall"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

const daemonChildEnv = "_HUMAN_DAEMON_CHILD"

// Log rotation keeps the append-mode files under ~/.human bounded while the
// daemon runs unattended (SC-2611). Diagnostic logs are size-capped and their
// oldest generations are discarded; the audit and destructive accountability
// trails rotate only for readability and never lose a generation to the daemon.
const (
	// diagnosticLogMaxBytes rotates a diagnostic log once it reaches 20 MB,
	// small enough to stay quick to grep while diagnosing a problem.
	diagnosticLogMaxBytes = 20 * 1024 * 1024
	// diagnosticLogGenerations bounds retained diagnostic history: with the size
	// cap this holds at most ~120 MB per log (1 live + 5 rotated) on disk.
	diagnosticLogGenerations = 5
	// accountabilityLogMaxBytes rotates audit.log/destructive.log at the same
	// size purely so an old trail stays readable; see accountability policy below.
	accountabilityLogMaxBytes = 20 * 1024 * 1024
)

// rotateDaemonLogs bounds the diagnostic logs and rotates the accountability
// trails for readability. Every failure is logged and none is fatal: an
// unrotated log costs disk, a daemon that exits over it costs the whole pipeline.
func rotateDaemonLogs(logger zerolog.Logger) {
	diagnostic := logrotate.Policy{
		MaxSizeBytes:   diagnosticLogMaxBytes,
		MaxGenerations: diagnosticLogGenerations,
	}
	// MaxGenerations 0 means never discard: the durable record lives in SQLite +
	// S3, but the on-disk trail is the local, offline copy and the unattended
	// daemon must not be the thing that deletes it (SC-2611 decision: option 2).
	accountability := logrotate.Policy{
		MaxSizeBytes:   accountabilityLogMaxBytes,
		MaxGenerations: 0,
	}
	targets := []struct {
		path   string
		policy logrotate.Policy
	}{
		{daemon.LogPath(), diagnostic},
		{ChromeBridgeLogPath(), diagnostic},
		{cmdutil.AuditLogPath(), accountability},
		{cmdutil.DestructiveLogPath(), accountability},
	}
	for _, t := range targets {
		if rotated, err := logrotate.Rotate(t.path, t.policy); err != nil {
			logger.Warn().Err(err).Str("path", t.path).Msg("log rotation failed")
		} else if rotated {
			logger.Info().Str("path", t.path).Msg("rotated log file")
		}
	}
}

// BuildDaemonCmd creates the "daemon" command tree.
func BuildDaemonCmd(cmdFactory func() *cobra.Command, version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run human as a daemon for remote (devcontainer) access",
	}

	cmd.AddCommand(buildDaemonStartCmd(cmdFactory, version))
	cmd.AddCommand(buildDaemonTokenCmd())
	cmd.AddCommand(buildDaemonStatusCmd())
	cmd.AddCommand(buildDaemonStopCmd())
	return cmd
}

func buildDaemonStartCmd(cmdFactory func() *cobra.Command, version string) *cobra.Command {
	var addr string
	var chromeAddr string
	var proxyAddr string
	var interactive bool
	var safe bool
	var debug bool
	var foreground bool
	var projectDirs []string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon listener",
		Long:  "Start the daemon on the host. AI agents inside devcontainers connect to this daemon to execute commands with the host's credentials.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interactive && !foreground && os.Getenv(daemonChildEnv) == "" {
				return errors.WithDetails("--interactive requires --foreground (needs stdin)")
			}

			if foreground || os.Getenv(daemonChildEnv) != "" {
				return runDaemonForeground(cmd, addr, chromeAddr, proxyAddr, interactive, safe, debug, projectDirs, cmdFactory, version)
			}
			return runDaemonBackground(cmd, addr, chromeAddr, proxyAddr, safe, debug, projectDirs)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:19285", "Listen address (host:port)")
	cmd.Flags().StringVar(&chromeAddr, "chrome-addr", "127.0.0.1:19286", "Chrome proxy listen address (host:port)")
	cmd.Flags().StringVar(&proxyAddr, "proxy-addr", "127.0.0.1:19287", "HTTPS proxy listen address (host:port)")
	cmd.Flags().BoolVar(&interactive, "interactive", false, "Prompt for unknown domains instead of blocking them")
	cmd.Flags().BoolVar(&safe, "safe", os.Getenv("HUMAN_SAFE") == "1", "Block destructive operations for all daemon requests")
	cmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging")
	cmd.Flags().BoolVar(&foreground, "foreground", false, "Run in foreground (don't daemonize)")
	cmd.Flags().StringArrayVar(&projectDirs, "project", nil, "Project directory to register (repeatable; defaults to cwd)")
	return cmd
}

// daemonState holds initialized daemon components before the main event loop.
type daemonState struct {
	srv           *daemon.Server
	ctx           context.Context
	stop          context.CancelFunc
	logger        zerolog.Logger
	connTracker   *daemon.ConnectedTracker
	networkStore  *daemon.NetworkEventStore
	modelSink     *daemon.ModelOutcomeSink
	agentIPs      *daemon.AgentIPRegistry
	vaultResolver *vault.Resolver
	statsStore    *stats.StatsStore
	costStore     *costledger.Store
	statsWriter   *stats.Writer
	auditStore    *audit.Store
	auditWriter   *audit.Writer
	confirmDB     *daemon.ConfirmDB
	ideationDB    *daemon.IdeationDB
	daemonID      string
	// info is the on-disk identity this process will claim once it is actually
	// serving. It is built here but written only after readiness, so a stalled
	// startup never records a process that is not yet answering.
	info daemon.DaemonInfo
}

// runMaintenanceLoop periodically cleans up stale pending confirmations and
// prunes the stats, audit, agent-execution-log, and agent-state stores past
// their retention windows. It runs until ctx is cancelled.
func runMaintenanceLoop(ctx context.Context, logger zerolog.Logger, confirmStore *daemon.PendingConfirmStore, statsStore *stats.StatsStore, auditStore *audit.Store, costStore *costledger.Store) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	// Agent state has a multi-day retention, so it gets its own slow ticker
	// rather than reopening SQLite every 30 seconds for nothing.
	stateTicker := time.NewTicker(time.Hour)
	defer stateTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runMaintenanceTick(ctx, logger, confirmStore, statsStore, auditStore, costStore)
		case <-stateTicker.C:
			pruneAgentState(ctx, logger)
		}
	}
}

// runMaintenanceTick is one pass of the fast maintenance sweep.
func runMaintenanceTick(ctx context.Context, logger zerolog.Logger, confirmStore *daemon.PendingConfirmStore, statsStore *stats.StatsStore, auditStore *audit.Store, costStore *costledger.Store) {
	confirmStore.Cleanup(daemon.ConfirmRetention)
	if statsStore != nil {
		if _, pruneErr := statsStore.Prune(ctx); pruneErr != nil {
			logger.Warn().Err(pruneErr).Msg("periodic stats prune failed")
		}
	}
	if costStore != nil {
		if _, pruneErr := costStore.Prune(ctx); pruneErr != nil {
			logger.Warn().Err(pruneErr).Msg("periodic cost ledger prune failed")
		}
	}
	if auditStore != nil {
		if _, pruneErr := auditStore.Prune(ctx); pruneErr != nil {
			logger.Warn().Err(pruneErr).Msg("periodic audit prune failed")
		}
	}
	if _, pruneErr := agent.PruneExecutions(); pruneErr != nil {
		logger.Warn().Err(pruneErr).Msg("periodic agent execution log prune failed")
	}
	rotateDaemonLogs(logger)
}

// pruneAgentState drops agent working state past its retention window. Every
// failure is logged and none is fatal: stale state costs disk, a daemon that
// exits over it costs the whole pipeline.
func pruneAgentState(ctx context.Context, logger zerolog.Logger) {
	store, err := agentstate.Open(agentstate.DefaultDBPath())
	if err != nil {
		logger.Warn().Err(err).Msg("failed to open agent state database for prune")
		return
	}
	defer func() { _ = store.Close() }()

	deleted, err := store.Prune(ctx, time.Now().UTC().Add(-agentstate.DefaultRetention))
	if err != nil {
		logger.Warn().Err(err).Msg("periodic agent state prune failed")
		return
	}
	if deleted > 0 {
		logger.Info().Int("deleted", deleted).Msg("pruned old agent state")
	}
}

// initAuditStore opens the audit database and starts its async writer, pruning
// stale events on startup. A failed open disables the trail (both returns nil)
// rather than aborting daemon startup.
func initAuditStore(ctx context.Context, logger zerolog.Logger) (*audit.Store, *audit.Writer) {
	store, err := audit.NewStore(audit.DefaultDBPath())
	if err != nil {
		logger.Warn().Err(err).Msg("failed to open audit database, audit trail disabled")
		return nil, nil
	}
	if deleted, pruneErr := store.Prune(ctx); pruneErr != nil {
		logger.Warn().Err(pruneErr).Msg("audit prune on startup failed")
	} else if deleted > 0 {
		logger.Info().Int64("deleted", deleted).Msg("pruned old audit events")
	}
	return store, audit.NewWriter(ctx, store, logger)
}

// initDaemon performs the early initialization steps for the daemon: token,
// PID file, project registry, daemon info, and signal context.
func initDaemon(cmd *cobra.Command, addr, chromeAddr, proxyAddr string, safe, debug bool, projectDirs []string, cmdFactory func() *cobra.Command, version string) (*daemonState, error) {
	token, err := daemon.LoadOrCreateToken()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "failed to load/create token")
	}

	// The daemon id stamps every machine-posted marker so a teammate can tell
	// which machine's bot acted (SC-660 rule 1). An operator-friendly override
	// via HUMAN_DAEMON_ID wins verbatim and is never persisted, so a readable
	// name (e.g. "alice-macbook") can replace the opaque persisted hex.
	daemonID := os.Getenv("HUMAN_DAEMON_ID")
	if daemonID == "" {
		daemonID, err = daemon.LoadOrCreateDaemonID()
		if err != nil {
			return nil, errors.WrapWithDetails(err, "failed to load/create daemon id")
		}
	}

	projectRegistry, projectInfos, err := buildProjectRegistry(projectDirs)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "failed to build project registry")
	}

	out := cmd.OutOrStdout()
	hostIP := resolveHostIP()
	daemonAddr := replaceHost(addr, hostIP)
	chromeFullAddr := replaceHost(chromeAddr, hostIP)
	proxyFullAddr := replaceHost(proxyAddr, hostIP)

	info := daemon.DaemonInfo{
		Addr:       daemonAddr,
		ChromeAddr: chromeFullAddr,
		ProxyAddr:  proxyFullAddr,
		Token:      token,
		PID:        os.Getpid(),
		Version:    version,
		Commit:     daemon.BuildRevision(),
		Protocol:   daemon.Protocol,
		DaemonID:   daemonID,
		Projects:   projectInfos,
	}

	printStartBanner(out, token, daemonID, addr, chromeAddr, proxyAddr, daemonAddr, chromeFullAddr, proxyFullAddr, projectInfos)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := newDaemonLogger(debug)
	// Before the first secret is resolved into this process: it is about to hold
	// every tracker credential on the machine, and a crash file or a same-user
	// debugger would publish all of them at once (SC-2183). The dumpable flag
	// resets on execve, so a self-restart child runs this again on its own way
	// up rather than inheriting the parent's protection.
	daemon.HardenProcess(logger)
	vaultResolver := buildVaultResolver(projectRegistry, logger)

	connTracker := daemon.NewConnectedTracker()
	// Persist hook events to the host so they survive the in-memory ring's
	// eviction and daemon restarts, keyed to the emitting agent's execution log.
	hookStore := daemon.NewHookEventStore().WithPersistence(agent.HookEventSink)
	networkStore := daemon.NewNetworkEventStore()
	// The model-call accounting boundary: the sink drains outcomes off the
	// request path, and the IP registry attributes each proxy connection to the
	// board agent (ticket+stage) that owns it (SC-2555).
	modelSink := daemon.NewModelOutcomeSink(ctx)

	// The durable per-ticket cost/time ledger (SC-2847). resolveProject is shared
	// verbatim by the sink (write) and the server (read) so a read filters by the
	// SAME project the write used — resolved per ticket, never a board-wide
	// default (AD5).
	costStore, resolveProject := initCostLedger(projectRegistry, modelSink, logger)
	agentIPs := daemon.NewAgentIPRegistry()
	confirmStore := daemon.NewPendingConfirmStore()
	// Approvals are durable: a restarted daemon re-offers undecided prompts
	// and honors unredeemed grants instead of silently dropping them. A failed
	// open degrades to memory-only rather than aborting startup.
	confirmDB, err := daemon.NewConfirmDB(daemon.DefaultConfirmDBPath())
	if err != nil {
		logger.Warn().Err(err).Msg("failed to open confirms database, approval persistence disabled")
		confirmDB = nil
	} else if err := confirmStore.WithPersistence(confirmDB, logger); err != nil {
		logger.Warn().Err(err).Msg("failed to load persisted approvals, approval persistence disabled")
		_ = confirmDB.Close()
		confirmDB = nil
	}

	// A live ideation chat is in-memory state that a restart would otherwise
	// reset; persisting it lets a self-restart land between turns harmlessly.
	// A failed open degrades to memory-only rather than aborting startup.
	ideationDB, err := daemon.NewIdeationDB(daemon.DefaultIdeationDBPath())
	if err != nil {
		logger.Warn().Err(err).Msg("failed to open ideation database, ideation sessions will not survive a restart")
		ideationDB = nil
	}
	// Assigned through a typed nil check: handing a nil *IdeationDB straight to
	// the interface would produce a non-nil interface wrapping a nil pointer,
	// and the engine's nil-Store check would not catch it.
	var ideationStore daemon.IdeationStore
	if ideationDB != nil {
		ideationStore = ideationDB
	}

	statsStore, err := stats.NewStatsStore(stats.DefaultDBPath())
	if err != nil {
		logger.Warn().Err(err).Msg("failed to open stats database, tool persistence disabled")
		statsStore = nil
	}

	var statsWriter *stats.Writer
	if statsStore != nil {
		// Prune old events on startup.
		if deleted, pruneErr := statsStore.Prune(ctx); pruneErr != nil {
			logger.Warn().Err(pruneErr).Msg("stats prune on startup failed")
		} else if deleted > 0 {
			logger.Info().Int64("deleted", deleted).Msg("pruned old tool events")
		}
		statsWriter = stats.NewWriter(ctx, statsStore, logger)
	}

	auditStore, auditWriter := initAuditStore(ctx, logger)

	go runMaintenanceLoop(ctx, logger, confirmStore, statsStore, auditStore, costStore)

	// Keep the shared code-navigation index fresh so every agent, worktree, and
	// the developer's CLI query one daemon-owned index instead of each rebuilding
	// it (SC-781).
	go runCodenavIndexLoop(ctx, projectRegistry, codenav.DefaultDBPath(), logger)

	doctor := daemon.NewDoctorRunner(buildDoctorChecks(projectRegistry, vaultResolver, doctorPersistence{
		stats:    statsStore != nil,
		audit:    auditStore != nil,
		confirms: confirmDB != nil,
	}))
	// launchGate lets the autonomous stage launcher refuse work when this host
	// fails a launch-critical doctor check (docker, agent-skills, claude-auth): it
	// leaves the handoff for a healthy daemon rather than claiming and failing it
	// (SC-912). Built from the same LaunchCriticalChecks the synchronous refusal
	// path uses; Blockers is nil-safe, so a doctor-less daemon disables cleanly.
	launchGate := func(ctx context.Context) []daemon.DoctorCheck {
		return doctor.Blockers(ctx, daemon.LaunchCriticalChecks)
	}
	// One fetcher instance, shared by the raw-results route and the composed
	// board: the closure carries the last-known cards and the last good listing
	// (1700, SC-2005), so a second instance would split that memory in half and
	// each route would degrade with only its own history.
	issueFetcher := fetchTrackerIssuesFunc(projectRegistry, vaultResolver, logger)

	// leaseChecker answers the daemon-busy route's "any live stage lease" half
	// by opening the shared agent-state database fresh per call — this route
	// only fires when a close is being considered (SC-3015), never on a hot
	// path, so a held-open connection buys nothing (mirrors pruneAgentState's
	// per-invocation open/close above).
	leaseChecker := func(ctx context.Context, project string) (bool, error) {
		store, err := agentstate.Open(agentstate.DefaultDBPath())
		if err != nil {
			return false, err
		}
		defer func() { _ = store.Close() }()
		leases, err := store.LiveLeases(ctx, project)
		if err != nil {
			return false, err
		}
		return len(leases) > 0, nil
	}

	srv := &daemon.Server{
		Addr:               addr,
		Token:              token,
		SafeMode:           safe,
		DaemonStartedAt:    time.Now().UTC(),
		CmdFactory:         cmdFactory,
		Logger:             logger,
		ConnectedPIDs:      connTracker,
		HookEvents:         hookStore,
		NetworkEvents:      networkStore,
		ModelOutcomes:      modelSink,
		CostLedger:         costStore,
		CostLedgerProject:  resolveProject,
		IssueFetcher:       issueFetcher,
		LiteIssueFetcher:   fetchTrackerIssuesLiteFunc(projectRegistry, vaultResolver),
		BoardViewFetcher:   boardViewFunc(issueFetcher, doctor, projectRegistry, boardcache.NewStore(boardcache.DefaultPath()), logger),
		IssueGetter:        daemon.NewCachedIssueGetter(issueGetterFunc(projectRegistry, vaultResolver)),
		CurrentUserFetcher: currentUserFunc(projectRegistry, vaultResolver),
		TrackerDiagnoser:   trackerDiagnoserFunc(projectRegistry, vaultResolver),
		Doctor:             doctor,
		Projects:           projectRegistry,
		PendingConfirms:    confirmStore,
		StatsWriter:        statsWriter,
		StatsStore:         statsStore,
		AuditSink:          auditWriter,
		AuditStore:         auditStore,
		AgentCleaner:       &dockerAgentCleaner{},
		VaultResolver:      vaultResolver,
		BoardTransitioner:  boardTransitionerFunc(projectRegistry, vaultResolver, daemonID, logger, launchGate, agentIPs),
		BoardFixer:         boardFixerFunc(projectRegistry, vaultResolver, daemonID, logger, launchGate, agentIPs),
		BoardSecurityFixer: securityFixerFunc(projectRegistry, vaultResolver, daemonID, logger, launchGate, agentIPs),
		BoardOptioner:      boardOptionerFunc(projectRegistry, vaultResolver, daemonID, logger, launchGate, agentIPs),
		BugCreator:         bugCreatorFunc(projectRegistry, vaultResolver, relateLauncherFunc(projectRegistry, daemonID)),
		WhereComments:      whereCommentsFunc(projectRegistry, vaultResolver),
		WhereAttempts: func(pmKey string, stage daemon.BoardStage) (int, error) {
			// The READ-ONLY twin of the retry path's counter. bumpStageRetries
			// increments as it reads, so wiring it here would spend a ticket's
			// budget every time an agent asked where it was.
			return readStageRetries(context.Background(), boardStateProject(projectRegistry, pmKey), pmKey, stage)
		},
		SecurityCreator:   securityCreatorFunc(projectRegistry, vaultResolver),
		CloseTicketer:     closeTicketerFunc(projectRegistry, vaultResolver, liveBoardAgents, (&dockerAgentCleaner{}).DeleteAgent, logger),
		FeaturesGenerator: featuresGeneratorFunc(projectRegistry),
		FindbugsRunner:    findbugsRunnerFunc(projectRegistry),
		RelateLauncher:    relateLauncherFunc(projectRegistry, daemonID),
		SecurityRunner:    securityRunnerFunc(projectRegistry),
		MockupsCreator:    mockupsCreatorFunc(projectRegistry),
		VariationsCreator: variationsCreatorFunc(projectRegistry),
		MockupChooser:     mockupChooserFunc(projectRegistry),
		MockupPruner:      mockupPrunerFunc(projectRegistry),
		Ideation:          ideationEngine(projectRegistry, vaultResolver, hookStore, ideationStore, logger),
		LeaseChecker:      leaseChecker,
	}

	// Bring back a chat the previous process was in the middle of, before the
	// server accepts its first ideation request.
	restoreIdeationSession(srv.Ideation, ideationStore, logger)

	return &daemonState{
		srv:           srv,
		ctx:           ctx,
		stop:          stop,
		logger:        logger,
		connTracker:   connTracker,
		networkStore:  networkStore,
		modelSink:     modelSink,
		agentIPs:      agentIPs,
		vaultResolver: vaultResolver,
		statsStore:    statsStore,
		costStore:     costStore,
		statsWriter:   statsWriter,
		auditStore:    auditStore,
		auditWriter:   auditWriter,
		confirmDB:     confirmDB,
		ideationDB:    ideationDB,
		daemonID:      daemonID,
		info:          info,
	}, nil
}

// runDaemonForeground runs the daemon in the current process (blocking).
// It writes a PID file on start and removes it on shutdown.
// initDaemonWithListeners binds (or, in a handover child, adopts) the three
// listeners and initializes daemon state, wiring the daemon listener into the
// server. On any failure it closes whatever it opened so no socket leaks.
func initDaemonWithListeners(cmd *cobra.Command, addr, chromeAddr, proxyAddr string, safe, debug bool, projectDirs []string, cmdFactory func() *cobra.Command, version string) (*daemonState, *listenerSet, error) {
	listeners, err := openListeners(addr, proxyAddr, chromeAddr)
	if err != nil {
		return nil, nil, err
	}
	ds, err := initDaemon(cmd, addr, chromeAddr, proxyAddr, safe, debug, projectDirs, cmdFactory, version)
	if err != nil {
		_ = listeners.daemon.Close()
		_ = listeners.proxy.Close()
		_ = listeners.chrome.Close()
		return nil, nil, err
	}
	ds.srv.Listener = listeners.daemon
	return ds, listeners, nil
}

// removeDaemonFilesUnlessHandedOver clears the pidfile and daemon.json on
// shutdown — but not after a self-restart handover, where the child now owns
// them and deleting would erase its freshly written files.
func removeDaemonFilesUnlessHandedOver(handedOver *atomic.Bool) {
	if handedOver.Load() {
		return
	}
	RemovePidFile()
	daemon.RemoveInfo()
}

// removeStatsFilesUnlessHandedOver clears the stats/connected files on shutdown,
// skipping the removal after a handover for the same reason.
func removeStatsFilesUnlessHandedOver(handedOver *atomic.Bool, statsPath, connectedPath string) {
	if handedOver.Load() {
		return
	}
	proxy.RemoveStats(statsPath)
	daemon.RemoveConnected(connectedPath)
}

// inflightMarker resolves a proxy connection's remote address to the agent
// name via ips (the same registry Attribute uses) and folds a +1/-1 delta
// into inflight. Pulled out of runDaemonForeground so the wiring's branch
// costs its own function rather than that one's complexity budget.
func inflightMarker(inflight *daemon.InflightModelRequests, ips *daemon.AgentIPRegistry) func(remoteAddr string, delta int) {
	return func(remoteAddr string, delta int) {
		if name, ok := ips.AgentFor(remoteAddr); ok {
			inflight.Mark(name, delta)
		}
	}
}

// agentProgressWithInflight wraps a hook-event-store progress probe, folding
// in the proxy's own outstanding-model-request signal (SC-3074): a request
// sent to the model and not yet answered is a positive sign of life the hook
// stream alone cannot see. Pulled out of runDaemonForeground so the wiring's
// branch costs its own function rather than that one's complexity budget.
func agentProgressWithInflight(hookEvents *daemon.HookEventStore, inflight *daemon.InflightModelRequests) daemon.AgentProgressProbe {
	return func(name string) (daemon.AgentProgress, bool) {
		p, ok := hookEvents.AgentProgress(name)
		if !ok {
			return p, false
		}
		p.OutstandingModelRequest = inflight.Outstanding(name)
		return p, true
	}
}

// startProxyServer builds the HTTPS proxy on the pre-owned listener, prints its
// one-line status, and serves it in the background. It returns the server so the
// stats writer can report on it.
func startProxyServer(ctx context.Context, proxyAddr string, interactive bool, logger zerolog.Logger, emitter proxy.NetworkEventEmitter, recorder proxy.ModelOutcomeRecorder, attribute proxy.ConnAttributor, markInflight func(remoteAddr string, delta int), ln net.Listener, out io.Writer) (*proxy.Server, error) {
	proxySrv, proxyStatus, err := buildProxyServer(proxyAddr, interactive, logger, emitter, recorder, attribute, markInflight)
	if err != nil {
		return nil, err
	}
	proxySrv.Listener = ln
	if proxyStatus != "" {
		_, _ = fmt.Fprintln(out, proxyStatus)
	}
	go func() {
		if err := proxySrv.ListenAndServe(ctx); err != nil {
			logger.Error().Err(err).Msg("https proxy failed")
		}
	}()
	return proxySrv, nil
}

// claimDaemonIdentity is called only once the listeners are accepting: it
// signals a handover parent to step down and records this process as the live
// daemon (pidfile + daemon.json). Deferring it out of runDaemonForeground keeps
// the identity claim off the readiness path — a locked vault can never freeze
// the process before it is recorded as serving (SC-2138).
func claimDaemonIdentity(ds *daemonState, logger zerolog.Logger) error {
	signalHandoverReady(logger)
	if err := WritePidFile(os.Getpid()); err != nil {
		return errors.WrapWithDetails(err, "failed to write PID file")
	}
	if err := daemon.WriteInfo(ds.info); err != nil {
		return errors.WrapWithDetails(err, "failed to write daemon info")
	}
	return nil
}

func runDaemonForeground(cmd *cobra.Command, addr, chromeAddr, proxyAddr string, interactive, safe, debug bool, projectDirs []string, cmdFactory func() *cobra.Command, version string) error {
	// Bind the daemon, chrome bridge, and HTTPS proxy on the interface
	// containers can reach without exposing them to the LAN (never 0.0.0.0): the
	// docker bridge gateway on native Linux Docker, loopback on Docker Desktop
	// (host.docker.internal forwards to loopback) and when Docker is down. An
	// explicit non-loopback override is respected. Doing this at startup means an
	// agent launch never has to restart the daemon mid-request for container
	// access — the sharp edge that used to abort the first containerized launch.
	reachHost := devcontainer.ContainerReachableHost()
	addr = swapLoopbackHost(addr, reachHost)
	chromeAddr = swapLoopbackHost(chromeAddr, reachHost)
	proxyAddr = swapLoopbackHost(proxyAddr, reachHost)

	// Own the three listeners here so a self-restart can hand the live sockets
	// to the re-exec'd child. On a handover child these adopt the inherited
	// sockets instead of binding, so no listener is ever torn down mid-rebuild.
	ds, listeners, err := initDaemonWithListeners(cmd, addr, chromeAddr, proxyAddr, safe, debug, projectDirs, cmdFactory, version)
	if err != nil {
		return err
	}

	// handedOver flips once a self-restart child owns the on-disk daemon state
	// (pidfile, daemon.json, stats files); the parent must then NOT delete them
	// on its way out, or it would erase the child's freshly written files.
	var handedOver atomic.Bool
	defer removeDaemonFilesUnlessHandedOver(&handedOver)
	defer ds.stop()
	if ds.statsWriter != nil {
		defer ds.statsWriter.Close()
	}
	if ds.statsStore != nil {
		defer func() { _ = ds.statsStore.Close() }()
	}
	// Store.Close is nil-safe, so no guard is needed (unlike statsStore above).
	defer func() { _ = ds.costStore.Close() }()
	if ds.auditWriter != nil {
		defer ds.auditWriter.Close()
	}
	if ds.auditStore != nil {
		defer func() { _ = ds.auditStore.Close() }()
	}
	if ds.confirmDB != nil {
		defer func() { _ = ds.confirmDB.Close() }()
	}
	if ds.ideationDB != nil {
		defer func() { _ = ds.ideationDB.Close() }()
	}

	out := cmd.OutOrStdout()
	ctx := ds.ctx
	logger := ds.logger

	chromeSvcs := startChromeServices(ctx, chromeAddr, ds.srv.Token, listeners.chrome, logger)

	// SC-3074: the daemon's own proxy sees every model request open and close,
	// so an outstanding one is a first-hand sign of life — unlike watching for
	// transcript output, which a thinking phase never produces. inflight is the
	// per-agent counter; markInflight resolves the proxy's remote address to
	// the agent name via the same registry Attribute uses.
	inflight := daemon.NewInflightModelRequests()
	markInflight := inflightMarker(inflight, ds.agentIPs)

	proxySrv, err := startProxyServer(ctx, proxyAddr, interactive, logger, ds.networkStore, ds.modelSink.Record, ds.agentIPs.Attribute, markInflight, listeners.proxy, out)
	if err != nil {
		return err
	}

	// The listeners are accepting, so this process is serving. Only now does it
	// claim the on-disk identity and (if it is a handover child) tell the parent
	// to step down. Doing this before any vault-touching startup work means a
	// locked vault can never freeze the child before it is recorded as the live
	// daemon — and a stalled child never leaves the pidfile/daemon.json naming a
	// process that is not answering (SC-2138).
	if err := claimDaemonIdentity(ds, logger); err != nil {
		return err
	}

	statsPath := proxy.StatsPath()
	connectedPath := daemon.ConnectedPath()
	statsDone := make(chan struct{})
	go func() {
		defer close(statsDone)
		writeDaemonStats(ctx, proxySrv, ds.connTracker, statsPath, connectedPath)
	}()
	// Wait for the stats writer to observe ctx cancellation and exit before
	// removing its files; otherwise a ticker tick can recreate them after
	// removal, leaving stale files that outlive the daemon.
	defer func() {
		<-statsDone
		removeStatsFilesUnlessHandedOver(&handedOver, statsPath, connectedPath)
	}()

	cwd, _ := os.Getwd()
	if unmount := fuseMount(cwd, safe, logger); unmount != nil {
		defer unmount()
	}

	// Slack, Telegram and the topology divergence warning all eagerly resolve
	// tracker/messaging secrets from the vault. That must not sit on the
	// readiness path: a locked vault would otherwise freeze the daemon before it
	// records itself as serving (SC-2138). They are best-effort and logged, so a
	// locked vault degrades them without holding up the daemon.
	go func() {
		// Turn a silent split->single topology fallback into a loud startup
		// signal: a tracker declared role: engineering whose token does not
		// resolve would run single-tracker here and split elsewhere from the same
		// config (SC-660 rule 7).
		warnTopologyDivergence(ds.srv.Projects, ds.vaultResolver, out, logger)

		slackNotifier, slackStatus := startSlackNotifier(logger, ds.vaultResolver)
		if slackStatus != "" {
			_, _ = fmt.Fprintln(out, "Slack notifications:", slackStatus)
		}

		telegramStatus := startTelegramDispatcher(ctx, logger, slackNotifier, ds.vaultResolver)
		_, _ = fmt.Fprintln(out, "Telegram dispatch:", telegramStatus)
	}()

	if err := claude.InstallHooks(out, claude.OSFileWriter{}); err != nil {
		logger.Warn().Err(err).Msg("hook upgrade failed")
	}

	go daemon.RunAgentCleanup(ctx, ds.srv.HookEvents, &dockerAgentCleaner{}, agentClaudeAlive, logger)
	hookEvents := ds.srv.HookEvents
	go daemon.RunAgentZombieSweep(ctx, &dockerAgentSweeper{}, agentProgressWithInflight(hookEvents, inflight), func(agentName string, reason daemon.ReapReason) {
		// A reaped agent died without firing hooks, so no exit event exists
		// for the board failure watcher to act on; synthesizing one converges
		// the reap path with the hook-driven exit paths — one marker-posting
		// code path (SC-206).
		//
		// A silence reap carries its reason as a sentinel ErrorType
		// ("reaped-silent:<idle>") so the exit handler routes it to the
		// uncharged relaunch instead of the charged failure path — the
		// machine's own judgement must not spend the ticket's retry budget
		// (SC-2447).
		errorType := ""
		if reason.Silent {
			errorType = daemon.ReapSilenceErrorType + ":" + reason.Idle.Round(time.Second).String()
		}
		hookEvents.Append(hookevents.Event{
			EventName: "StopFailure",
			AgentName: agentName,
			ErrorType: errorType,
			Timestamp: time.Now().UTC(),
		})
	}, logger)
	startSleepInhibitor(ctx, out, logger)
	// The auto-review chain runs through the same launch gate: a daemon that
	// cannot serve a review must leave the ready-for-review handoff unclaimed for
	// one that can, not claim and fail it (SC-912). Doctor.Blockers is nil-safe.
	reviewLaunchGate := func(ctx context.Context) []daemon.DoctorCheck {
		return ds.srv.Doctor.Blockers(ctx, daemon.LaunchRefusalChecks())
	}
	boardTransition := boardTransitionerFunc(ds.srv.Projects, ds.vaultResolver, ds.daemonID, logger, reviewLaunchGate, ds.agentIPs)
	boardRetryTransition := boardRetryTransitionerFunc(ds.srv.Projects, ds.vaultResolver, ds.daemonID, logger, reviewLaunchGate, ds.agentIPs)
	// A finished build chains straight into its review — the board's
	// auto-review; the transition engine re-derives and validates. Shared by
	// the live hook path (RunBoardFailureWatch) and the durable restart-recovery
	// path (RunBoardReconcile), each supplying the cause that names WHY it fired
	// (SC-2462): the live path is a prompt chain, the durable path is the poll
	// boundary that recovers a restart-orphaned handoff — the exact 31-minute
	// hole SC-2462 could not previously name.
	chainReviewWith := func(cause daemon.WaitCause) func(pmKey string) error {
		return func(pmKey string) error {
			return boardTransition(daemon.BoardTransitionRequest{
				PMKey: pmKey,
				From:  daemon.BoardImplementation,
				To:    daemon.BoardVerification,
				Cause: cause,
			})
		}
	}
	liveChainReview := chainReviewWith(daemon.WaitCauseChain)
	durableChainReview := chainReviewWith(daemon.WaitCausePollBoundary)
	// The diagnoser reads the dead run's persisted artifacts so the failed
	// marker says what actually broke instead of the generic stage line.
	diagnoseFailure := func(agentName, hookErrorType string) daemon.FailureDiagnosis {
		d := agent.DiagnoseFailure(agentName, hookErrorType)
		return daemon.FailureDiagnosis{Headline: d.Headline, Detail: d.Detail}
	}
	// The pre-merge PR review→fix loop is driven off the reviewer/fixer Stop
	// events, exactly like chainReviewWith: on each loop-agent exit read the outcome
	// it recorded (the reviewer's verdict, the fixer's exit) from the state store
	// and hand it to the loop executor, which decides the next step.
	advancePRLoop := advancePRLoopFunc(ctx, ds, diagnoseFailure, reviewLaunchGate, logger)
	// The deploy-fixer (SC-1557) is driven off its Stop event exactly like the PR
	// loop: on exit read the exit it recorded in stage.deploy-fix and hand it to
	// AdvanceDeployFix, which re-runs Deploy on `done` or reds the card otherwise.
	advanceDeployFix := advanceDeployFixFunc(ctx, ds, reviewLaunchGate, logger)
	// A daemon only chains a review for a handoff branch it can resolve on its
	// own machine — a board-context fix leaves its branch local on the machine
	// that produced it, so a daemon elsewhere leaves the handoff for one that can
	// reach it (SC-652). The board operates on the single registered project.
	branchReachable := func(branch string) daemon.ProbeResult {
		return boardBranchReachable(ctx, ds.srv.Projects, branch)
	}
	// A handoff must name commits the branch actually contains — a retry that never
	// pushed its work named SHAs no machine could see (735). This gate verifies
	// every named commit is reachable from the branch on this machine (local ref or
	// origin/<branch>); any absent commit fails the check.
	commitsPresent := func(branch string, commits []string) daemon.ProbeResult {
		return boardCommitsPresent(ctx, ds.srv.Projects, branch, commits)
	}
	// A cleanly finished stage is the only thing that authorizes reclaiming the
	// run's private worktree; every other exit keeps the work for forensics
	// (SC-731). MarkHandoff is best-effort/idempotent.
	onHandoff := func(agentName string) { agent.MarkHandoff(agentName) }
	// stageRetry lets a stage that failed for a reason another attempt could fix
	// — a flaky check, a container that died — relaunch itself instead of waiting
	// for someone to click Retry. It reads the exit class the agent recorded in
	// agent state and issues the same in-place retry transition the Retry gesture
	// does, so every existing guard (idempotency, the cross-daemon claim arbiter,
	// the per-stage prompt) applies unchanged.
	stageRetry := daemon.StageRetry{
		Max: daemon.DefaultStageRetries,
		Outcome: func(pmKey string, stage daemon.BoardStage) (daemon.StageExit, bool) {
			return stageExitClass(ctx, boardStateProject(ds.srv.Projects, pmKey), pmKey, stage, logger)
		},
		Attempts: func(pmKey string, stage daemon.BoardStage) (int, error) {
			return bumpStageRetries(ctx, boardStateProject(ds.srv.Projects, pmKey), pmKey, stage)
		},
		Reset: func(pmKey string, stage daemon.BoardStage) {
			clearStageRetries(ctx, boardStateProject(ds.srv.Projects, pmKey), pmKey, stage)
		},
		Relaunch: func(pmKey string, stage daemon.BoardStage) (bool, error) {
			// From is unused by the in-place retry rules; the card's own derived
			// stage plus its failed state is what selects the retry path. The
			// launched bool is what tells a real relaunch from a refusal that
			// started nothing, so a refusal is never charged an attempt (SC-2989).
			return boardRetryTransition(daemon.BoardTransitionRequest{PMKey: pmKey, From: stage, To: stage})
		},
		Uncount: func(pmKey string, stage daemon.BoardStage) {
			decStageRetries(ctx, boardStateProject(ds.srv.Projects, pmKey), pmKey, stage)
		},
	}
	go daemon.RunBoardFailureWatch(ctx, ds.srv.HookEvents,
		boardPMCommenterFunc(ds.srv.Projects, ds.vaultResolver, ds.daemonID),
		liveChainReview, liveBoardAgents, advancePRLoop, advanceDeployFix, branchReachable, commitsPresent, diagnoseFailure, onHandoff, stageRetry, ds.modelSink.LatestClass, ds.daemonID, logger)
	// The live chain fires only on the one-shot exit hook; this pass re-scans
	// comments to recover a handoff orphaned by a daemon restart or lost hook
	// (SC-430).
	// Confirm-shipped probe (SC-910): a done-stage deploy-failure whose PR the
	// forge reports merged is cleared by posting a [human:deployed] marker.
	prMerged := func(probeCtx context.Context, prURL string) (bool, error) {
		return boardPRMerged(probeCtx, ds.srv.Projects, ds.vaultResolver, prURL)
	}
	postDeployed := func(postCtx context.Context, pmKey, prURL string) error {
		commenter, err := boardPMCommenterFunc(ds.srv.Projects, ds.vaultResolver, ds.daemonID)()
		if err != nil {
			return err
		}
		_, err = commenter.AddComment(postCtx, pmKey,
			daemon.DeployedHeader+"\npr: "+prURL)
		return err
	}
	// A live container is not a working agent. The hook stream is the progress
	// signal — a crashed AND a hung agent both stop emitting — so the reconcile
	// pass asks it before deciding a stage is stuck.
	agentProgress := agentProgressWithInflight(ds.srv.HookEvents, inflight)
	// A hung agent still holds its container and workspace; it must be stopped
	// before any relaunch, or two agents work the same stage.
	stopHungAgent := func(agentName string) error {
		stopCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		return (&dockerAgentCleaner{}).DeleteAgent(stopCtx, agentName)
	}
	go daemon.RunBoardReconcile(ctx,
		boardReconcileListerFunc(ds.srv.Projects, ds.vaultResolver, logger),
		// A nil participation predicate means "participate in every visible
		// project" — the backward-compatible default. A machine that opts into a
		// narrower set supplies a predicate here (SC-2047 opt-in participation).
		branchReachable, boardParticipation(ds.srv.Projects), commitsPresent, prMerged, postDeployed,
		liveBoardAgents, postFailedMarkerFunc(ds.srv.Projects, ds.vaultResolver, ds.daemonID),
		closedTicketProbeFunc(ds.srv.Projects, ds.vaultResolver),
		// The durable re-drive has no exiting agent to attribute — the run it is
		// recovering from is long gone — so it drives the loop with no run identity
		// and any escalation falls back to its generic line.
		durableChainReview, func(pmKey string) error { return advancePRLoop(pmKey, "", "") },
		stageRetry, agentProgress, stopHungAgent, ds.daemonID, daemon.BoardReconcileInterval, logger)

	// Surface tickets created or edited outside the board (tracker web UI, CLI,
	// another teammate or daemon) — none raise a board event — by polling the
	// cheap titles-only listing and poking subscribers on any change. Gated on
	// live subscribers so it costs nothing when no board is open.
	go daemon.RunBoardFreshnessPoll(ctx,
		ds.srv.LiteIssueFetcher, ds.srv.PokeBoard,
		boardHasWatchers(ds.srv.HookEvents),
		daemon.BoardFreshnessInterval, logger)

	// Keep the searchable ticket record current, so an agent's "is this already
	// being handled?" check has something to find. It was fed only by a hand-run
	// command and held nothing for months (SC-2132).
	go RunRecallSync(ctx, ds.srv.Projects, ds.vaultResolver, recall.DefaultDBPath(), RecallSyncInterval, logger)

	// Watch the binary so a rebuild re-execs into the new build, handing over the
	// live sockets (no client sees a refused connection) and draining in-flight
	// connections before the old process exits. A no-op on Windows.
	//
	// The drain covers proxied agent egress and chrome-side sessions alike; the
	// retire hook removes this process's PID-named relay socket the moment the
	// successor is up, so a client globbing the socket directory cannot pick the
	// dying daemon's socket.
	handover := handoverHooks{
		activeConns: func() int64 { return proxySrv.ActiveConns() + chromeSvcs.activeConns() },
		retire:      chromeSvcs.retire,
	}
	// The watcher's failure path restores this parent's identity from ds.info, so
	// a child that never comes up leaves the pidfile/daemon.json naming the
	// survivor (SC-2138).
	maybeWatchBinary(ctx, listeners, ds.srv, handover, ds.stop, &handedOver, ds.info, logger)

	return ds.srv.ListenAndServe(ctx)
}

// liveBoardAgents lists the names of currently running agents so the durable
// stuck-running reconcile pass (1136) can tell a live-but-slow run from a
// dead-ended card. It reads the same running-agent metadata the zombie sweep
// does; a card whose stage agent is absent here has no live owner.
func liveBoardAgents() ([]string, error) {
	metas, err := agent.ListMetas()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range metas {
		if m.Status != agent.StatusRunning {
			continue
		}
		names = append(names, m.Name)
	}
	return names, nil
}

// postFailedMarkerFunc builds the reconcile pass's marker poster. A stuck-running
// card is reddened by posting its stage's *-failed marker on the PM ticket,
// moving it to a needs-attention Retry badge. Stamped with the daemon id
// (SC-660 rule 1) so the poster is attributable.
func postFailedMarkerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string) func(context.Context, string, string) error {
	return func(postCtx context.Context, pmKey, body string) error {
		commenter, err := boardPMCommenterFunc(reg, resolver, daemonID)()
		if err != nil {
			return err
		}
		_, err = commenter.AddComment(postCtx, pmKey, body)
		return err
	}
}

// chromeServices are the running chrome-side listeners a self-restart has to
// account for: the TCP bridge (its socket is handed to the child) and the Unix
// socket relay (whose PID-named socket the outgoing daemon must retire).
type chromeServices struct {
	relay  *chrome.SocketRelay
	server *chrome.Server
}

// activeConns reports chrome-side connections still in flight, so a handover
// drains them rather than cutting Chrome off mid-session. Nil-safe: chrome
// services that failed to start contribute nothing.
func (c chromeServices) activeConns() int64 {
	var n int64
	if c.relay != nil {
		n += c.relay.ActiveConns()
	}
	if c.server != nil {
		n += c.server.ActiveConns()
	}
	return n
}

// retire stops the outgoing daemon's relay from accepting and removes its
// socket file, so a client globbing the socket directory finds only the
// successor's. Nil-safe.
func (c chromeServices) retire() {
	if c.relay != nil {
		c.relay.Retire()
	}
}

// startChromeServices launches the socket relay and Chrome MCP proxy. The
// chromeLn listener, when non-nil, is served instead of binding chromeAddr, so
// a self-restart hands the bridge's live socket to the re-exec'd child.
func startChromeServices(ctx context.Context, chromeAddr, token string, chromeLn net.Listener, logger zerolog.Logger) chromeServices {
	socketDir, sdErr := chrome.SocketDir()
	if sdErr != nil {
		logger.Warn().Err(sdErr).Msg("resolving socket directory")
		return chromeServices{}
	}

	relay := chrome.NewSocketRelay(socketDir, logger)
	go func() {
		if err := relay.ListenAndServe(ctx); err != nil {
			logger.Error().Err(err).Msg("socket relay failed")
		}
	}()

	claudePath, lookErr := exec.LookPath("claude")
	if lookErr != nil {
		logger.Warn().Err(lookErr).Msg("claude not found in PATH, chrome proxy will fail on connection")
	}

	chromeSrv := &chrome.Server{
		Addr:  chromeAddr,
		Token: token,
		Translator: &chrome.McpTranslator{
			ClaudePath: claudePath,
			Logger:     logger,
		},
		Logger:   logger,
		Listener: chromeLn,
	}

	go func() {
		if err := chromeSrv.ListenAndServe(ctx); err != nil {
			logger.Error().Err(err).Msg("chrome proxy server failed")
		}
	}()

	return chromeServices{relay: relay, server: chromeSrv}
}

// runDaemonBackground re-execs the current binary as a detached child process.
func runDaemonBackground(cmd *cobra.Command, addr, chromeAddr, proxyAddr string, safe, debug bool, projectDirs []string) error {
	out := cmd.OutOrStdout()

	// Check if already running.
	if pid, alive := ReadAlivePid(); alive {
		_, _ = fmt.Fprintf(out, "Daemon is already running (PID %d)\n", pid)
		return nil
	}

	logPath := DaemonLogPath()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- logPath is built by DaemonLogPath(), not user input
	if err != nil {
		return errors.WrapWithDetails(err, "opening log file", "path", logPath)
	}

	exe, err := os.Executable()
	if err != nil {
		_ = logFile.Close()
		return errors.WrapWithDetails(err, "resolving executable path")
	}

	args := []string{"daemon", "start", "--foreground",
		"--addr", addr,
		"--chrome-addr", chromeAddr,
		"--proxy-addr", proxyAddr,
	}
	if safe {
		args = append(args, "--safe")
	}
	if debug {
		args = append(args, "--debug")
	}
	for _, dir := range projectDirs {
		args = append(args, "--project", dir)
	}

	child := exec.Command(exe, args...) // #nosec G204 -- re-exec of own binary via os.Executable()
	child.Env = append(os.Environ(), daemonChildEnv+"=1")
	child.Stderr = logFile
	child.Stdout = logFile
	child.SysProcAttr = detachSysProcAttr()

	if err := child.Start(); err != nil {
		_ = logFile.Close()
		return errors.WrapWithDetails(err, "starting background process")
	}
	_ = logFile.Close()

	pid := child.Process.Pid

	// Detach so we don't wait for the child.
	_ = child.Process.Release()

	// The child (runDaemonForeground → initDaemon) binds the container-reachable
	// host, so poll and advertise that same address rather than a bare loopback.
	bindAddr := swapLoopbackHost(addr, devcontainer.ContainerReachableHost())

	// Poll for TCP readiness (up to 3s).
	const (
		pollInterval = 50 * time.Millisecond
		pollTimeout  = 3 * time.Second
	)
	deadline := time.Now().Add(pollTimeout)
	ready := false
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", bindAddr, 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(pollInterval)
	}

	hostIP := resolveHostIP()
	daemonAddr := replaceHost(bindAddr, hostIP)

	if !ready {
		_, _ = fmt.Fprintf(out, "Daemon started (PID %d) but not yet reachable\n", pid)
		_, _ = fmt.Fprintf(out, "  Log: %s\n", logPath)
		return nil
	}

	token, tokenErr := daemon.LoadOrCreateToken()
	if tokenErr != nil {
		return errors.WrapWithDetails(tokenErr, "loading daemon token")
	}
	tokenPrefix := token
	if len(token) >= 8 {
		tokenPrefix = token[:8]
	}
	chromeFullAddr := replaceHost(chromeAddr, hostIP)
	proxyFullAddr := replaceHost(proxyAddr, hostIP)

	_, _ = fmt.Fprintf(out, "Daemon started (PID %d)\n", pid)
	_, _ = fmt.Fprintln(out, "  Listening on:", daemonAddr)
	_, _ = fmt.Fprintf(out, "  Log: %s\n", logPath)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "Run in the container:")
	_, _ = fmt.Fprintf(out, "  export HUMAN_DAEMON_ADDR=%s HUMAN_DAEMON_TOKEN=%s... HUMAN_CHROME_ADDR=%s HUMAN_PROXY_ADDR=%s\n",
		daemonAddr, tokenPrefix, chromeFullAddr, proxyFullAddr)
	_, _ = fmt.Fprintln(out, "  # Full token: human daemon token")
	return nil
}

func buildDaemonTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print the current daemon token",
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := daemon.LoadOrCreateToken()
			if err != nil {
				return errors.WrapWithDetails(err, "failed to load/create token")
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), token)
			return nil
		},
	}
}

func buildDaemonStatusCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check if a daemon is reachable",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			pid, pidAlive := ReadAlivePid()

			// Read once and keep it: the address, and the build the daemon
			// reports for itself.
			info, infoErr := daemon.ReadInfo()
			if infoErr == nil && !cmd.Flags().Changed("addr") {
				addr = info.Addr
			}

			conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				if pidAlive {
					_, _ = fmt.Fprintf(out, "Daemon is running (PID %d) but not reachable at %s\n", pid, addr)
				} else {
					_, _ = fmt.Fprintln(out, "Daemon is not running")
				}
				return errors.WrapWithDetails(err, "cannot connect to daemon", "addr", addr)
			}
			_ = conn.Close()

			if pidAlive {
				_, _ = fmt.Fprintf(out, "Daemon is running (PID %d) and reachable at %s\n", pid, addr)
			} else {
				_, _ = fmt.Fprintln(out, "Daemon is reachable at", addr)
			}
			// The daemon reports its own build because hardening closes off the
			// /proc entry this used to be read from (SC-2183). Without it, "is
			// the daemon running the code I just built?" has no answer.
			if info.Commit != "" {
				_, _ = fmt.Fprintf(out, "Build: %s (%s)\n", info.Version, info.Commit)
			}
			// A failed self-restart handover leaves the old build serving while a
			// newer binary sits on disk. Surface that here — comparing the running
			// daemon's build against the build of the binary running this status
			// command (which is by definition the one on disk) — so the silent
			// failure becomes visible where someone will look (SC-2138).
			if notice := staleBuildNotice(info.Commit, daemon.BuildRevision()); notice != "" {
				_, _ = fmt.Fprintln(out, notice)
			}

			// Show registered projects if available.
			if info, err := daemon.ReadInfo(); err == nil && len(info.Projects) > 0 {
				_, _ = fmt.Fprintf(out, "Projects: %d\n", len(info.Projects))
				for _, p := range info.Projects {
					_, _ = fmt.Fprintf(out, "  %s (%s)\n", p.Name, p.Dir)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:19285", "Daemon address to check")
	return cmd
}

// staleBuildNotice reports that the running daemon is an older build than the
// one on disk — the visible symptom of a failed self-restart handover (SC-2138).
// It stays empty unless both revisions are known and differ, so a daemon with no
// recorded build (or a status binary with no VCS stamp) never produces a false
// alarm.
func staleBuildNotice(runningCommit, onDiskCommit string) string {
	if runningCommit == "" || onDiskCommit == "" || runningCommit == onDiskCommit {
		return ""
	}
	return fmt.Sprintf(
		"WARNING: the daemon is running an older build (%s) than the one on disk (%s); a self-restart likely failed — run `human daemon stop && human daemon start`",
		runningCommit, onDiskCommit)
}

func buildDaemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop a running daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			pid, alive := ReadAlivePid()
			if !alive {
				_, _ = fmt.Fprintln(out, "Daemon is not running")
				RemovePidFile()
				daemon.RemoveInfo()
				return nil
			}

			_, _ = fmt.Fprintf(out, "Stopping daemon (PID %d)...\n", pid)
			if err := stopProcess(pid); err != nil {
				return errors.WrapWithDetails(err, "failed to stop daemon", "pid", pid)
			}

			// Poll for exit (up to 5s).
			const (
				pollInterval = 100 * time.Millisecond
				pollTimeout  = 5 * time.Second
			)
			deadline := time.Now().Add(pollTimeout)
			for time.Now().Before(deadline) {
				if !isProcessAlive(pid) {
					break
				}
				time.Sleep(pollInterval)
			}

			if isProcessAlive(pid) {
				return errors.WithDetails("daemon did not exit within timeout", "pid", pid)
			}

			RemovePidFile()
			daemon.RemoveInfo()
			_, _ = fmt.Fprintln(out, "Daemon stopped")
			return nil
		},
	}
}

// --- PID file helpers (delegated to internal/daemon) ---

// DaemonLogPath returns the path to the daemon log file.
func DaemonLogPath() string { return daemon.LogPath() }

// DaemonPidPath returns the path to the daemon PID file.
func DaemonPidPath() string { return daemon.PidPath() }

// WritePidFile writes the PID to the PID file.
func WritePidFile(pid int) error { return daemon.WritePidFile(pid) }

// RemovePidFile removes the PID file.
func RemovePidFile() { daemon.RemovePidFile() }

// ReadAlivePid reads the PID file and checks if the process is alive.
// Returns (0, false) if no PID file exists or the process is dead.
func ReadAlivePid() (int, bool) { return daemon.ReadAlivePid() }

// resolveHostIP returns the preferred outbound IP of the host.
// Falls back to "localhost" if detection fails.
func resolveHostIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "localhost"
	}
	defer func() { _ = conn.Close() }()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "localhost"
	}
	return addr.IP.String()
}

// startTelegramDispatcher starts the Telegram dispatch loop if a Telegram
// instance is configured. It runs as a background goroutine and returns
// a human-readable status string for the startup banner.
func startTelegramDispatcher(ctx context.Context, logger zerolog.Logger, extraNotifier dispatch.Notifier, resolver *vault.Resolver) string {
	configs, cfgErr := telegram.LoadConfigs(".")
	if cfgErr != nil {
		logger.Warn().Err(cfgErr).Msg("failed to load Telegram config, dispatch disabled")
		return "error loading config"
	}
	if len(configs) == 0 {
		return "not configured (add telegrams: to .humanconfig)"
	}

	var instances []telegram.Instance
	var err error
	if resolver != nil {
		instances, err = telegram.LoadInstancesWithResolver(".", resolver.Resolve)
	} else {
		instances, err = telegram.LoadInstances(".")
	}
	if err != nil {
		logger.Warn().Err(err).Msg("failed to build Telegram instances")
		return "error loading config"
	}
	if len(instances) == 0 {
		names := make([]string, len(configs))
		for i, c := range configs {
			names[i] = c.Name
		}
		logger.Warn().Strs("instances", names).Msg("Telegram configured but token missing — set TELEGRAM_<NAME>_TOKEN")
		return fmt.Sprintf("missing token (set TELEGRAM_%s_TOKEN)", strings.ToUpper(configs[0].Name))
	}

	inst := instances[0]

	// Surface config health warnings before we start the dispatcher so
	// misconfigurations (e.g. Telegram enabled with an empty allowlist,
	// which silently rejects every message) are visible to the operator
	// at startup, not just in retrospect via the rejection counter.
	for _, w := range inst.ConfigWarnings() {
		logger.Warn().Msg(w)
	}

	runner := claude.OSCommandRunner{}
	homeDir, _ := os.UserHomeDir()

	d := &dispatch.Dispatcher{
		Source: &dispatch.TelegramSource{
			Client:       inst.Client,
			AllowedUsers: inst.AllowedUsers,
			AllowedChats: inst.AllowedChats,
			Logger:       logger,
		},
		Finder: &dispatch.TmuxAgentFinder{
			InstanceFinder: &claude.HostFinder{Runner: runner, HomeDir: homeDir},
			TmuxClient:     &claude.OSTmuxClient{Runner: runner},
			ProcessLister:  &claude.OSProcessLister{Runner: runner},
		},
		Sender:   &dispatch.TmuxSender{Runner: runner},
		Notifier: buildNotifier(&dispatch.TelegramNotifier{Client: inst.Client}, extraNotifier),
		Config:   dispatch.Config{PollInterval: dispatch.DefaultPollInterval},
		Logger:   logger,
	}

	go func() {
		if err := d.Run(ctx); err != nil {
			logger.Error().Err(err).Msg("telegram dispatcher failed")
		}
	}()

	logger.Info().Str("telegram", inst.Name).Msg("telegram dispatch enabled")
	return fmt.Sprintf("enabled (%s)", inst.Name)
}

// startSlackNotifier creates a Slack notifier if configured.
// Returns (nil, "") when Slack is not configured (no error — it is optional).
func startSlackNotifier(logger zerolog.Logger, resolver *vault.Resolver) (dispatch.Notifier, string) {
	configs, cfgErr := slack.LoadConfigs(".")
	if cfgErr != nil {
		logger.Warn().Err(cfgErr).Msg("failed to load Slack config, notifications disabled")
		return nil, "error loading config"
	}
	if len(configs) == 0 {
		return nil, ""
	}

	var instances []slack.Instance
	var err error
	if resolver != nil {
		instances, err = slack.LoadInstancesWithResolver(".", resolver.Resolve)
	} else {
		instances, err = slack.LoadInstances(".")
	}
	if err != nil {
		logger.Warn().Err(err).Msg("failed to build Slack instances")
		return nil, "error loading config"
	}
	if len(instances) == 0 {
		logger.Warn().Str("instance", configs[0].Name).Msg("Slack configured but token missing")
		return nil, fmt.Sprintf("missing token (set SLACK_%s_TOKEN)", strings.ToUpper(configs[0].Name))
	}

	inst := instances[0]
	logger.Info().Str("slack", inst.Name).Msg("slack notifications enabled")
	return &dispatch.SlackNotifier{Client: inst.Client}, fmt.Sprintf("enabled (%s)", inst.Name)
}

// buildNotifier wraps a primary notifier with an optional extra notifier.
func buildNotifier(primary dispatch.Notifier, extra dispatch.Notifier) dispatch.Notifier {
	if extra == nil {
		return primary
	}
	return &dispatch.CompositeNotifier{Notifiers: []dispatch.Notifier{primary, extra}}
}

// writeDaemonStats periodically writes proxy stats and connected PIDs to disk for the TUI.
func writeDaemonStats(ctx context.Context, proxySrv *proxy.Server, tracker *daemon.ConnectedTracker, proxyPath, connectedPath string) {
	const connectedTTL = 30 * time.Second
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = proxy.WriteStats(proxyPath, proxy.Stats{ActiveConns: proxySrv.ActiveConns()})
			tracker.Prune(connectedTTL)
			_ = daemon.WriteConnected(connectedPath, tracker.PIDs())
		}
	}
}

// buildProxyServer creates the HTTPS proxy server with policy and optional
// MITM interceptor. Returns a status string for the startup banner.
// emitter is injected so the proxy can publish ambient network activity to
// the daemon's in-memory store without circular imports.
func buildProxyServer(addr string, interactive bool, logger zerolog.Logger, emitter proxy.NetworkEventEmitter, recorder proxy.ModelOutcomeRecorder, attribute proxy.ConnAttributor, markInflight func(remoteAddr string, delta int)) (*proxy.Server, string, error) {
	proxyCfg, _ := proxy.LoadConfig(".")

	var policy proxy.Decider
	var err error
	if proxyCfg != nil {
		policy, err = proxy.NewPolicy(proxyCfg.Mode, proxyCfg.Domains)
		if err != nil {
			return nil, "", errors.WrapWithDetails(err, "invalid proxy policy")
		}
	} else {
		policy = proxy.BlockAllPolicy()
	}

	var status string
	if interactive {
		prompt := proxy.NewTerminalPrompt(os.Stdin, os.Stderr)
		policy = proxy.NewInteractiveDecider(policy, prompt)
		status = "Interactive proxy mode: unknown domains will prompt for approval\n"
	}

	// The agent container bind-mounts ~/.human/ca.crt and points
	// NODE_EXTRA_CA_CERTS at it. Generate the CA up front — even when
	// intercept is off — so the file always exists as real PEM before any
	// container starts; otherwise Docker fabricates an empty directory at the
	// bind source and Node's PEM parse fails on every run.
	if home, herr := os.UserHomeDir(); herr == nil {
		humanDir := filepath.Join(home, ".human")
		if _, _, _, caErr := proxy.LoadOrCreateCA(humanDir); caErr != nil {
			logger.Warn().Err(caErr).Msg("failed to pre-generate proxy CA")
		}
	}

	interceptor, interceptStatus := buildInterceptor(proxyCfg, logger, recorder, attribute, markInflight)
	if interceptStatus != "" {
		status += interceptStatus
	}

	srv := &proxy.Server{
		Addr:        addr,
		Policy:      policy,
		Interceptor: interceptor,
		Logger:      logger,
		Emitter:     emitter,
	}

	return srv, status, nil
}

// buildInterceptor creates a MITM logging interceptor if intercept domains
// are configured. Returns (nil, "") when not configured.
func buildInterceptor(proxyCfg *proxy.Config, logger zerolog.Logger, recorder proxy.ModelOutcomeRecorder, attribute proxy.ConnAttributor, markInflight func(remoteAddr string, delta int)) (proxy.Interceptor, string) {
	if proxyCfg == nil || len(proxyCfg.Intercept) == 0 {
		return nil, ""
	}

	home, _ := os.UserHomeDir()
	humanDir := filepath.Join(home, ".human")

	caCert, caKey, _, err := proxy.LoadOrCreateCA(humanDir)
	if err != nil {
		logger.Error().Err(err).Msg("failed to load/create CA, intercept disabled")
		return nil, "MITM intercept: disabled (CA error)"
	}

	logDir := filepath.Join(humanDir, "llm-traffic")
	interceptor := &proxy.LoggingInterceptor{
		Domains:   proxyCfg.Intercept,
		LeafCache: &proxy.LeafCache{CACert: caCert, CAKey: caKey},
		Logger:    logger,
		LogDir:    logDir,
		// The SC-2555 accounting seam: content-free outcome recording, gated on
		// the model-API host and attributed by connection source. nil-safe both,
		// so a proxy without a daemon sink still MITMs and logs unchanged.
		RecordOutcome: recorder,
		Attribute:     attribute,
		// SC-3074: the outstanding-model-request signal the zombie sweep and
		// board reconcile fold into AgentProgress so a thinking run — no hook
		// event, no transcript output — still reads as working.
		InflightModelRequests: markInflight,
	}

	return interceptor, fmt.Sprintf("MITM intercept: %v\n  CA cert: %s\n  Traffic logs: %s",
		proxyCfg.Intercept, filepath.Join(humanDir, "ca.crt"), logDir)
}

// newDaemonLogger creates a zerolog console logger at the appropriate level.
func newDaemonLogger(debug bool) zerolog.Logger {
	level := zerolog.InfoLevel
	if debug {
		level = zerolog.DebugLevel
	}
	return zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger().Level(level)
}

// printStartBanner prints the daemon startup information.
func printStartBanner(out io.Writer, token, daemonID, addr, chromeAddr, proxyAddr, daemonAddr, chromeFullAddr, proxyFullAddr string, projects []daemon.ProjectInfo) {
	_, _ = fmt.Fprintln(out, "Token:", token)
	_, _ = fmt.Fprintln(out, "Token file:", daemon.TokenPath())
	_, _ = fmt.Fprintln(out, "Daemon ID:", daemonID)
	_, _ = fmt.Fprintln(out, "Listening on:", addr)
	_, _ = fmt.Fprintln(out, "Chrome proxy on:", chromeAddr)
	_, _ = fmt.Fprintln(out, "HTTPS proxy on:", proxyAddr)
	if len(projects) > 0 {
		_, _ = fmt.Fprintf(out, "Projects: %d\n", len(projects))
		for _, p := range projects {
			_, _ = fmt.Fprintf(out, "  %s (%s)\n", p.Name, p.Dir)
		}
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "Run in the container:")
	_, _ = fmt.Fprintf(out, "  export HUMAN_DAEMON_ADDR=%s HUMAN_DAEMON_TOKEN=%s... HUMAN_CHROME_ADDR=%s HUMAN_PROXY_ADDR=%s\n",
		daemonAddr, token[:8], chromeFullAddr, proxyFullAddr)
	_, _ = fmt.Fprintln(out, "  # Full token: human daemon token")
	_, _ = fmt.Fprintf(out, "  export BROWSER=human-browser\n")
	_, _ = fmt.Fprintln(out, "  ln -sf $(which human) /usr/local/bin/human-browser  # if not already installed")
}

// buildProjectRegistry creates a ProjectRegistry from the given dirs,
// defaulting to cwd when no dirs are specified.
func buildProjectRegistry(dirs []string) (*daemon.ProjectRegistry, []daemon.ProjectInfo, error) {
	if len(dirs) == 0 {
		cwd, _ := os.Getwd()
		dirs = []string{cwd}
	}

	reg, err := daemon.NewProjectRegistry(dirs)
	if err != nil {
		return nil, nil, err
	}

	var infos []daemon.ProjectInfo
	for _, e := range reg.Entries() {
		infos = append(infos, daemon.ProjectInfo(e))
	}
	return reg, infos, nil
}

// buildVaultResolver reads the vault config from the first registered project
// and creates a session-scoped vault resolver. Returns nil if vault is not
// configured (graceful no-op — plain tokens continue to work).
func buildVaultResolver(reg *daemon.ProjectRegistry, logger zerolog.Logger) *vault.Resolver {
	for _, entry := range reg.Entries() {
		cfg, err := vault.ReadConfig(entry.Dir)
		if err != nil {
			logger.Warn().Err(err).Str("project", entry.Name).Msg("vault config parse failed; resolution disabled for this project")
			continue
		}
		if cfg == nil {
			continue
		}
		resolver := vault.NewResolverFromConfig(cfg)
		if resolver != nil {
			logger.Info().Str("provider", cfg.Provider).Str("project", entry.Name).Msg("vault secret resolution enabled")
			return resolver
		}
	}
	return nil
}

// swapLoopbackHost replaces a loopback or empty host in addr with reachHost —
// the interface containers can reach the daemon on. An explicit non-loopback
// host (an operator's --addr override, or a bridge gateway carried over from a
// restart) is left untouched, so it never silently widens a deliberate bind.
func swapLoopbackHost(addr, reachHost string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "127.0.0.1" || host == "localhost" {
		return net.JoinHostPort(reachHost, port)
	}
	return addr
}

// replaceHost replaces an empty or wildcard host in addr with the given host.
// e.g. ":19285" → "192.168.1.5:19285", "0.0.0.0:19285" → "192.168.1.5:19285".
func replaceHost(addr, host string) string {
	h, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		return net.JoinHostPort(host, port)
	}
	return addr
}

// fetchTrackerIssuesFunc returns an IssueFetcher that loads tracker instances
// from all registered project directories using per-project env scoping and
// vault secret resolution.
// trackerDiagnoserFunc returns a function that diagnoses tracker status by
// actually loading instances through the vault resolver. Only trackers that
// successfully load (credentials resolved and valid) are reported as working.
func trackerDiagnoserFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) func(dir string) []tracker.TrackerStatus {
	return func(dir string) []tracker.TrackerStatus {
		// Get the config-level view (what's configured).
		configured := tracker.DiagnoseTrackers(dir, config.UnmarshalSection, os.Getenv)

		// Find the project entry for this dir to get env scoping.
		entry, ok := reg.Resolve(dir)
		if !ok {
			return configured
		}

		// Actually load instances through vault resolution.
		loaded, err := cmdutil.LoadAllInstancesWithResolver(entry.Dir, entry.EnvLookup(), resolver)
		if err != nil {
			// Vault or loading failed — mark all as not working.
			for i := range configured {
				configured[i].Working = false
			}
			return configured
		}

		// Build set of loaded instance keys.
		loadedSet := make(map[string]bool) // "kind/name"
		for _, inst := range loaded {
			loadedSet[inst.Kind+"/"+inst.Name] = true
		}

		// Only mark as working if the instance actually loaded.
		for i := range configured {
			key := configured[i].Kind + "/" + configured[i].Name
			configured[i].Working = loadedSet[key]
		}
		return configured
	}
}

// warnTopologyDivergence turns a silent split->single topology fallback into a
// loud startup signal (SC-660 rule 7). For each registered project it compares
// the topology the config DECLARES (a tracker carrying role: engineering) with
// the topology its RESOLVABLE credentials can actually run; a declared
// engineering tracker whose token does not resolve would run single-tracker here
// and split elsewhere from the same config. The daemon still starts (one
// misconfigured project must not take down a multi-project daemon), but the
// divergence is logged at error level and printed on the startup banner so an
// operator cannot miss it.
func warnTopologyDivergence(reg *daemon.ProjectRegistry, resolver *vault.Resolver, out io.Writer, logger zerolog.Logger) {
	if reg == nil {
		return
	}
	for _, entry := range reg.Entries() {
		declared := tracker.DiagnoseTrackers(entry.Dir, config.UnmarshalSection, os.Getenv)
		instances, err := cmdutil.LoadAllInstancesWithResolver(entry.Dir, entry.EnvLookup(), resolver)
		if err != nil {
			logger.Warn().Err(err).Str("project", entry.Name).
				Msg("topology check: cannot load instances")
			continue
		}
		resolvedEngineering := false
		for _, inst := range instances {
			if inst.InferRole() == "engineering" {
				resolvedEngineering = true
				break
			}
		}
		if err := tracker.ValidateTopology(declared, resolvedEngineering); err != nil {
			logger.Error().Err(err).Str("project", entry.Name).
				Msg("topology divergence: engineering-role tracker declared but not resolved")
			_, _ = fmt.Fprintf(out, "WARNING: topology divergence in %s: %s\n", entry.Name, err.Error())
		}
	}
}

// fetchJob pairs a configured tracker instance with a specific project to
// fetch. Lifted out of the closure so helpers (scanReadyForReview) can
// reference the same type.
type fetchJob struct {
	inst    tracker.Instance
	project string
	dir     string
}

func fetchTrackerIssuesFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, logger zerolog.Logger) func() ([]daemon.TrackerIssuesResult, error) {
	// lastCards persists across board fetches so a per-ticket fetch failure can
	// carry the ticket's last-known stage forward (degraded) instead of demoting
	// it to Backlog (1700). Guarded by mu for the concurrent scan goroutines.
	var mu sync.Mutex
	lastCards := make(map[string]daemon.BoardCard)
	lastKnown := func(key string) (daemon.BoardCard, bool) {
		mu.Lock()
		defer mu.Unlock()
		c, ok := lastCards[key]
		return c, ok
	}
	// lastResults is the newest listing that actually produced issues. A refresh
	// that fails outright serves this instead of nothing: an empty board is
	// indistinguishable from having no work at all, which is the false
	// impression the whole board is built to avoid (SC-2005). The failure still
	// rides along as an error result, so the board says it is stale rather than
	// quietly presenting old cards as current.
	var lastResults []daemon.TrackerIssuesResult
	return func() ([]daemon.TrackerIssuesResult, error) {
		jobs, results, err := listTrackerIssues(reg, resolver)
		if err != nil {
			mu.Lock()
			stale := lastResults
			mu.Unlock()
			served, serveErr := staleListing(stale, err)
			if serveErr == nil {
				logger.Warn().Err(err).Msg("board fetch failed; serving the last good listing marked stale")
			}
			return served, serveErr
		}

		// Scan PM-tracker comments for [human:ready-for-review] handoffs and
		// per-PM board state, then propagate them onto the results. See
		// cli/CLAUDE.md "Review handoff".
		readyKeys, readyPRs, boardCards := scanReadyForReview(jobs, results, logger, lastKnown)

		// Remember every card we could actually derive this scan; a later fetch
		// error for one of these keys carries its stage forward rather than
		// dropping the ticket to Backlog.
		mu.Lock()
		for k, c := range boardCards {
			if !c.Degraded {
				lastCards[k] = c
			}
		}
		mu.Unlock()

		applyScanResults(results, readyKeys, readyPRs, boardCards)
		// Only a listing that actually produced issues is worth falling back to:
		// remembering an all-error listing would let one bad refresh become the
		// "last good" one and pin the board empty.
		if anyIssues(results) {
			mu.Lock()
			lastResults = results
			mu.Unlock()
		}
		return results, nil
	}
}

// anyIssues reports whether a listing carried at least one issue.
func anyIssues(results []daemon.TrackerIssuesResult) bool {
	for _, r := range results {
		if len(r.Issues) > 0 {
			return true
		}
	}
	return false
}

// staleListing decides what a failed refresh should serve. With a remembered
// listing it serves that, plus an error result announcing the staleness — the
// board keeps its cards AND says they are not current. With nothing remembered
// it surfaces the failure, because there is nothing truer to show than "this
// did not work".
//
// The announcement is not optional: trading a blank board for a silently stale
// one that looks healthy would replace a visible problem with an invisible one.
// The remembered listing is copied so a caller cannot mutate the fallback that
// later refreshes still depend on.
func staleListing(stale []daemon.TrackerIssuesResult, err error) ([]daemon.TrackerIssuesResult, error) {
	if len(stale) == 0 {
		return nil, err
	}
	out := make([]daemon.TrackerIssuesResult, len(stale), len(stale)+1)
	copy(out, stale)
	return append(out, daemon.TrackerIssuesResult{
		Project: "refresh",
		Err:     "showing the last successful fetch — this refresh failed: " + err.Error(),
	}), nil
}

// fetchTrackerIssuesLiteFunc returns a fetcher that lists issue titles only,
// skipping the per-ticket comment scan (scanReadyForReview) that dominates board
// latency. Results carry Issues but no BoardCards, so the desktop board can show
// titles immediately and reconcile stages once the full fetcher completes.
func fetchTrackerIssuesLiteFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) func() ([]daemon.TrackerIssuesResult, error) {
	return func() ([]daemon.TrackerIssuesResult, error) {
		_, results, err := listTrackerIssues(reg, resolver)
		return results, err
	}
}

// issueGetterFunc builds the daemon's IssueGetter closure: it resolves the
// tracker instance named in the request and fetches the single full issue.
// The per-key fetch exists because list endpoints on some trackers (e.g.
// Shortcut) return slim payloads without descriptions.
func issueGetterFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) func(daemon.IssueDetailRequest) (*daemon.IssueDetailFetch, error) {
	return func(req daemon.IssueDetailRequest) (*daemon.IssueDetailFetch, error) {
		entry, err := reg.EntryForKey(req.Key)
		if err != nil {
			return nil, err
		}
		instances, err := cmdutil.LoadAllInstancesWithResolver(entry.Dir, entry.EnvLookup(), resolver)
		if err != nil {
			return nil, err
		}
		// Resolve by kind+name when the kind is known: a name alone is
		// ambiguous when different provider sections share one instance name.
		var inst *tracker.Instance
		if req.Kind != "" {
			inst, err = tracker.ResolveByKind(req.Kind, instances, req.Tracker)
		} else {
			inst, err = tracker.Resolve(req.Tracker, instances, req.Key)
		}
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		issue, err := inst.Provider.GetIssue(ctx, req.Key)
		if err != nil {
			return nil, err
		}
		// AD-4: the comment-sourced extras are best-effort. A ListComments
		// error (or a tracker blip) degrades to empty extras so the panel
		// still shows the issue body rather than failing the whole request.
		var extras daemon.IssueDetailExtras
		if comments, cerr := inst.Provider.ListComments(ctx, req.Key); cerr == nil {
			extras = daemon.BuildIssueDetailExtras(comments)
		}
		return &daemon.IssueDetailFetch{Issue: *issue, Extras: extras}, nil
	}
}

// boardViewFunc builds the daemon's composed-board fetcher: the same listing the
// raw route serves, run through board.Compose so every consumer reads one
// picture instead of assembling its own.
//
// Docker availability is read from THIS host's doctor check rather than probed
// by whoever renders the board. Docker matters because it is where agents
// launch, which is this machine — a client probing its own engine answers a
// question about the wrong computer (SC-2132).
func boardViewFunc(fetch func() ([]daemon.TrackerIssuesResult, error), doctor *daemon.DoctorRunner, reg *daemon.ProjectRegistry, cache *boardcache.Store, logger zerolog.Logger) func() (daemon.BoardView, error) {
	return func() (daemon.BoardView, error) {
		project := boardProjectKey(reg)
		results, err := fetch()
		if err != nil {
			return serveLastGoodView(cache, project, err, logger)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dockerOK := len(doctor.Blockers(ctx, []string{"docker"})) == 0
		view := board.Compose(results, dockerOK)
		attachActivity(ctx, reg, &view, logger)
		rememberBoardView(cache, project, view, logger)
		return view, nil
	}
}

// initCostLedger opens the durable per-ticket cost/time ledger and wires it into
// the sink, returning the store (nil on a failed open — accounting degrades to
// memory-only rather than aborting startup) and the per-ticket project resolver
// shared with the server's read path (SC-2847 AD5).
func initCostLedger(projectRegistry *daemon.ProjectRegistry, modelSink *daemon.ModelOutcomeSink, logger zerolog.Logger) (*costledger.Store, func(string) string) {
	resolveProject := func(ticket string) string {
		entry, err := projectRegistry.EntryForKey(ticket)
		if err != nil {
			return ""
		}
		return entry.Dir
	}
	costStore, err := costledger.NewStore(costledger.DefaultDBPath())
	if err != nil {
		logger.Warn().Err(err).Msg("failed to open cost ledger, per-ticket cost/time disabled")
		return nil, resolveProject
	}
	modelSink.WithLedger(costStore, resolveProject, logger)
	return costStore, resolveProject
}

// boardProjectKey names the project a board snapshot belongs to, keyed the same
// way the desktop keyed it: the first registered project's directory. The
// per-project key is load-bearing — a global one evicted another project's
// snapshot on every save (SC-1654/1692).
func boardProjectKey(reg *daemon.ProjectRegistry) string {
	if entries := reg.Entries(); len(entries) > 0 {
		return entries[0].Dir
	}
	return ""
}

// serveLastGoodView answers a failed compose with the last board that worked,
// marked stale, rather than with nothing.
//
// An empty board is indistinguishable from having no work at all, which is the
// impression the whole board exists to avoid — so a refresh that cannot complete
// must not present as a clean, empty workspace. This supersedes the
// results-level fallback (SC-2005) by sitting one layer up: it survives a
// failure to COMPOSE, not only a failure to fetch.
//
// Nothing remembered means nothing truer to show than the failure itself.
func serveLastGoodView(cache *boardcache.Store, project string, cause error, logger zerolog.Logger) (daemon.BoardView, error) {
	raw, ok := cache.Load(project)
	if !ok {
		return daemon.BoardView{}, cause
	}
	var view daemon.BoardView
	if err := json.Unmarshal(raw, &view); err != nil {
		return daemon.BoardView{}, cause
	}
	logger.Warn().Err(cause).Msg("board view: refresh failed, serving the last good board marked stale")
	view.Error = "showing the last board that loaded — this refresh failed: " + cause.Error()
	return view, nil
}

// rememberBoardView keeps the last board that actually carried work, so a later
// failure has something true to fall back on. A board with no cards is not
// remembered: one bad refresh would otherwise become the "last good" one and pin
// the board empty for good. Best-effort — a cache write must never fail a fetch.
func rememberBoardView(cache *boardcache.Store, project string, view daemon.BoardView, logger zerolog.Logger) {
	if len(view.Cards) == 0 {
		return
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return
	}
	if err := cache.Save(project, raw); err != nil {
		logger.Debug().Err(err).Msg("board view: cannot persist the last good board")
	}
}

// shouldReportLoadFailure reports whether a load failure is news. A held-off
// secret failure is the vault's backoff working: the store was not consulted at
// all, and the first real failure already logged the full cause chain. Logging
// it again on every 30s refresh is the flood this fix is about (SC-3322).
func shouldReportLoadFailure(err error) bool { return !vault.IsHeldOff(err) }

// logReportableLoadFailures logs every failure that is news, skipping the
// held-off ones the vault's own backoff already reported once. Every failure
// still counts as a load failure at the call site regardless of whether it is
// logged here, so the board keeps reporting itself as degraded either way.
//
// subsystem names the caller (e.g. "board listing", "recall sync") so the one
// diagnostic channel this fix exists to clean up still tells the reader which
// loop hit the failure, rather than attributing every caller's failures to
// whichever subsystem happened to be hardcoded here.
func logReportableLoadFailures(failures []error, dir, subsystem string) {
	for _, failure := range failures {
		if !shouldReportLoadFailure(failure) {
			continue
		}
		// LogError renders the full cause chain AND the attached details (the
		// secret reference, op's stderr, the exit code). Only the outermost
		// message survives to the board banner, so the log is the one place
		// the whole diagnosis lands — without it a recurring credential blip
		// leaves nothing to debug after the fact (SC-2005).
		errors.LogError(failure).Str("dir", dir).
			Msg(subsystem + ": tracker instances failed to load, continuing without them")
	}
}

// listingJobs is the (instance, project) work one registered project contributes
// to a board listing — the decision of what the daemon asks a tracker for, kept
// apart from the fetching so it can be checked without a tracker or a disk.
//
// Every instance is asked. There is no longer anything here that might not hold
// tickets: forges are a separate list of a separate type, so the guards this
// function used to carry — one for a declared forge, one for a GitHub entry that
// declared nothing — have no subject ([SC-3876]). Between them those two guards
// took SC-1671, SC-2132 and SC-3868 to get right.
//
// An instance with no configured projects still yields one job, with an empty
// project, because most trackers read that as "everything I can see" — which is
// the whole listing for a single-project backend.
func listingJobs(instances []tracker.Instance, dir string) []fetchJob {
	var jobs []fetchJob
	for _, inst := range instances {
		projects := inst.Projects
		if len(projects) == 0 {
			projects = []string{""}
		}
		for _, p := range projects {
			jobs = append(jobs, fetchJob{inst: inst, project: p, dir: dir})
		}
	}
	return jobs
}

// listTrackerIssues collects every (instance, project) pair from the registry and
// fetches their open issues in parallel (Phase 1). It returns the jobs aligned 1:1
// with the results so a later comment scan can recover each result's provider
// without re-loading instances from disk.
func listTrackerIssues(reg *daemon.ProjectRegistry, resolver *vault.Resolver) ([]fetchJob, []daemon.TrackerIssuesResult, error) {
	// Collect all (instance, project) pairs first.
	var jobs []fetchJob
	// loadFailures are credential/config failures that cost us whole instances.
	// They are carried to the end and appended as visible error results rather
	// than aborting: one provider's momentary credential failure used to erase
	// every OTHER provider's cards, blanking the board (SC-2005). Secrets are
	// deliberately never cached, so this load runs on every refresh and any blip
	// took the whole board down with it.
	var loadFailures []error
	for _, entry := range reg.Entries() {
		instances, failures := cmdutil.LoadAllInstancesTolerant(entry.Dir, entry.EnvLookup(), resolver)
		logReportableLoadFailures(failures, entry.Dir, "board listing")
		loadFailures = append(loadFailures, failures...)
		jobs = append(jobs, listingJobs(instances, entry.Dir)...)
	}

	// Fetch all tracker/project combinations in parallel.
	results := make([]daemon.TrackerIssuesResult, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func(i int, job fetchJob) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			page, fetchErr := tracker.ListIssuesPage(ctx, job.inst.Provider, tracker.ListOptions{
				Project: job.project,
				// A ticket the board cannot fetch is a ticket silently lost —
				// the cap must comfortably exceed any real open backlog. The
				// per-ticket comment scan this once bounded stays cheap: idea
				// tickets skip it entirely and the rest fan out concurrently.
				// When a backend does hit this cap it reports it via
				// IssuePage.Truncated, which the board threads into its prune
				// guard and a "showing the first N" affordance (SC-1693) so the
				// truncation is visible and never silently discards saved state.
				MaxResults: 200,
				IncludeAll: false,
			})
			label := job.project
			if label == "" {
				label = job.inst.Name
			}
			results[i] = daemon.TrackerIssuesResult{
				TrackerName: job.inst.Name,
				TrackerKind: job.inst.Kind,
				TrackerRole: job.inst.InferRole(),
				Project:     label,
				Issues:      page.Issues,
				Truncated:   page.Truncated,
			}
			if fetchErr != nil {
				results[i].Err = fetchErr.Error()
			}
		}(i, job)
	}
	wg.Wait()

	// A tracker we could not even build carries no issues, so it appends as a
	// pure error result: the board shows the failure without losing the
	// trackers that did load. No fetchJob is added for it — jobs and results
	// stay 1:1, and the comment-scan fan-out skips it on the empty role.
	for _, err := range loadFailures {
		results = append(results, daemon.TrackerIssuesResult{
			Project: "credentials",
			Err:     err.Error(),
		})
		jobs = append(jobs, fetchJob{})
	}

	// Record which project each PM-role key was fetched from so keyed
	// board-action closures can route a request to its owning project
	// instead of defaulting to a fixed one (SC-1694). Skip error results —
	// an origin from a failed fetch would be stale by construction.
	var origins []daemon.KeyOrigin
	for i := range results {
		if results[i].TrackerRole != "pm" || results[i].Err != "" {
			continue
		}
		for _, iss := range results[i].Issues {
			origins = append(origins, daemon.KeyOrigin{Key: iss.Key, Dir: jobs[i].dir})
		}
	}
	reg.SetOrigins(origins)

	return jobs, results, nil
}

// applyScanResults projects the comment-scan output back onto the fetched
// results: board cards land on PM-role results (keyed by PM issue key) while
// ready-for-review keys and PR URLs land on engineering-role results. Extracted
// from fetchTrackerIssuesFunc to keep that closure within complexity bounds.
func applyScanResults(results []daemon.TrackerIssuesResult, readyKeys map[string]bool, readyPRs map[string]string, boardCards map[string]daemon.BoardCard) {
	for i := range results {
		switch results[i].TrackerRole {
		case "pm":
			for _, iss := range results[i].Issues {
				card, ok := boardCards[iss.Key]
				if !ok {
					continue
				}
				if results[i].BoardCards == nil {
					results[i].BoardCards = make(map[string]daemon.BoardCard)
				}
				results[i].BoardCards[iss.Key] = card
			}
		case "engineering":
			for _, iss := range results[i].Issues {
				if !readyKeys[iss.Key] {
					continue
				}
				results[i].ReadyForReview = append(results[i].ReadyForReview, iss.Key)
				if pr := readyPRs[iss.Key]; pr != "" {
					if results[i].ReadyForReviewPRs == nil {
						results[i].ReadyForReviewPRs = make(map[string]string)
					}
					results[i].ReadyForReviewPRs[iss.Key] = pr
				}
			}
		}
	}
}

// scanReadyForReview walks PM-tracker results, fetches each issue's comments,
// and returns the set of engineering ticket keys currently flagged ready for
// review. A newer [human:review-complete] comment on the same issue clears
// earlier handoffs for that issue.
//
// jobs and results are aligned 1:1 so we can recover the tracker.Provider for
// a given result without re-loading instances from disk.
// cards maps each PM issue key to its derived BoardCard. It is built from the
// same fetched comments, so no additional tracker round-trip is needed.
//
// lastKnown, when non-nil, returns the previously derived (non-degraded) card
// for a key. A ListComments failure carries that card's stage/state forward,
// flagged degraded, instead of silently dropping the key — a missing key
// otherwise renders downstream as an indistinguishable, actionable Backlog
// card (1700).
func scanReadyForReview(jobs []fetchJob, results []daemon.TrackerIssuesResult, logger zerolog.Logger, lastKnown func(key string) (daemon.BoardCard, bool)) (map[string]bool, map[string]string, map[string]daemon.BoardCard) {
	ready := make(map[string]bool)
	prs := make(map[string]string)
	cards := make(map[string]daemon.BoardCard)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range results {
		if results[i].TrackerRole != "pm" || results[i].Err != "" {
			continue
		}
		commenter, ok := jobs[i].inst.Provider.(tracker.Commenter)
		if !ok {
			continue
		}
		for _, issue := range results[i].Issues {
			// Idea tickets are placed by their label alone — no marker scan
			// needed, so skip the per-issue comment round-trip entirely.
			if issue.IsIdea() {
				mu.Lock()
				cards[issue.Key] = daemon.DeriveBoardCard(nil, issue.StatusType, true)
				mu.Unlock()
				continue
			}
			wg.Add(1)
			// Capture StatusType alongside Key so DeriveBoardCard can decide
			// the empty-Backlog-vs-Hidden case for a marker-less ticket.
			go func(c tracker.Commenter, key string, statusType tracker.Category) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				comments, err := c.ListComments(ctx, key)
				if err != nil {
					// A per-ticket fetch failure must not silently drop the key:
					// downstream a missing card renders as an actionable Backlog
					// card indistinguishable from never-worked, letting a user
					// launch a second pipeline on live work (1700). Log the cause
					// and emit an explicit degraded card, carrying the last-known
					// stage forward when we have one.
					logger.Warn().Str("key", key).Err(err).
						Msg("board comment fetch failed; rendering degraded card")
					card := daemon.BoardCard{Degraded: true}
					if lastKnown != nil {
						if prev, ok := lastKnown(key); ok {
							card = prev
							card.Degraded = true
							// A read failure is not evidence the plan vanished: keep the
							// last-known HasPlan so an unreachable tracker never reads as
							// "no plan exists" and invites re-planning a ticket a human
							// already planned (SC-2307 AC3). When there is no lastKnown,
							// HasPlan stays genuinely unknown and Degraded=true renders the
							// card non-launchable — the honest, invitation-suppressing state.
						}
					}
					mu.Lock()
					cards[key] = card
					mu.Unlock()
					return
				}
				card := daemon.DeriveBoardCard(comments, statusType, false)
				keys, pr := latestReadyKeys(comments)
				mu.Lock()
				cards[key] = card
				for _, k := range keys {
					ready[k] = true
					if pr != "" {
						prs[k] = pr
					}
				}
				mu.Unlock()
			}(commenter, issue.Key, issue.StatusType)
		}
	}
	wg.Wait()
	return ready, prs, cards
}

// latestReadyKeys walks a comment thread and returns the engineering keys
// from the most recent [human:ready-for-review] comment (and the pull-request
// URL on its optional pr: line, if any), unless a later
// [human:review-complete] comment has already superseded it.
func latestReadyKeys(comments []tracker.Comment) ([]string, string) {
	// Find the most recent handoff and the most recent review-complete.
	var latestHandoff tracker.Comment
	var latestComplete tracker.Comment
	var haveHandoff, haveComplete bool
	for _, c := range comments {
		switch {
		case daemon.IsReviewComplete(c.Body):
			if !haveComplete || c.Created.After(latestComplete.Created) {
				latestComplete = c
				haveComplete = true
			}
		case len(daemon.ParseEngineeringKeysFromHandoff(c.Body)) > 0:
			if !haveHandoff || c.Created.After(latestHandoff.Created) {
				latestHandoff = c
				haveHandoff = true
			}
		}
	}
	if !haveHandoff {
		return nil, ""
	}
	// Inclusive boundary: tracker timestamps are second-granular, so a
	// review-complete posted in the same second as the handoff must still
	// clear it (otherwise the (R) annotation lingers after review is done).
	if haveComplete && !latestComplete.Created.Before(latestHandoff.Created) {
		return nil, ""
	}
	return daemon.ParseEngineeringKeysFromHandoff(latestHandoff.Body), daemon.ParsePRFromHandoff(latestHandoff.Body)
}

// dockerAgentCleaner implements daemon.AgentCleaner using a real Docker client.
type dockerAgentCleaner struct{}

func (c *dockerAgentCleaner) DeleteAgent(ctx context.Context, name string) error {
	docker, err := devcontainer.NewDockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = docker.Close() }()

	mgr := &agent.Manager{Docker: docker}
	return mgr.Delete(ctx, name)
}

func (c *dockerAgentCleaner) DecommissionAgent(name string) (string, error) {
	meta, err := agent.ReadMeta(name)
	if err != nil {
		return "", err
	}
	containerID := meta.ContainerID
	// The async decommission path force-removes the container by id via
	// StopContainer *after* this function has deleted the meta, bypassing
	// stopLocked's copy-out. Copy the transcript out and record the outcome here
	// while the meta (and thus container id + agent name) still exists (SC-216).
	if containerID != "" {
		if docker, dErr := devcontainer.NewDockerClient(); dErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			agent.PreserveExecutionArtifacts(ctx, docker, meta, "reaped")
			cancel()
			_ = docker.Close()
		}
	}
	_ = agent.DeleteMeta(name)
	_ = devcontainer.DeleteMeta(name)
	return containerID, nil
}

func (c *dockerAgentCleaner) StopContainer(ctx context.Context, containerID string) error {
	docker, err := devcontainer.NewDockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = docker.Close() }()

	timeout := 2
	_ = docker.ContainerStop(ctx, containerID, &timeout)
	return docker.ContainerRemove(ctx, containerID, devcontainer.ContainerRemoveOptions{Force: true})
}

// dockerAgentLauncher implements daemon.AgentLauncher by starting a
// devcontainer-based agent. It mirrors cmdagent.newManager and the existing
// dockerAgentCleaner. Board launches set SkipPerms:true so the agent runs with
// --dangerously-skip-permissions (required for unattended pipeline work).
type dockerAgentLauncher struct {
	// daemonID reaches the container as HUMAN_DAEMON_ID so agent-posted markers
	// (ready-for-review, plan-ready) are attributed to this machine's bot like
	// the daemon-posted ones (SC-660 rule 1).
	daemonID string
	// agentIPs, when set, learns the launched container's bridge IP so a model
	// call arriving at the proxy from that IP attributes to this agent's
	// ticket+stage (SC-2555). nil disables the mapping; accounting still records
	// the call, just without attribution.
	agentIPs *daemon.AgentIPRegistry
}

func (l dockerAgentLauncher) Launch(ctx context.Context, name, prompt, workspace, configDir string) error {
	docker, err := devcontainer.NewDockerClient()
	if err != nil {
		return errors.WrapWithDetails(err, "connecting to Docker for board agent", "agent", name)
	}
	defer func() { _ = docker.Close() }()

	mgr := &agent.Manager{Docker: docker}
	meta, err := mgr.Start(ctx, agent.StartOpts{
		Name:      name,
		Prompt:    prompt,
		SkipPerms: true,
		Workspace: workspace,
		ConfigDir: configDir,
		DaemonID:  l.daemonID,
	})
	if err == nil {
		l.registerAgentIP(ctx, docker, meta.ContainerID, name)
	}
	return translateLaunchErr(err)
}

// containerInspector is the sliver of the Docker client registerAgentIP needs:
// resolving a container's network address. Narrowed to an interface so the
// attribution wiring is unit-testable without a full Docker fake.
type containerInspector interface {
	ContainerInspect(ctx context.Context, containerID string) (devcontainer.ContainerInspectResponse, error)
}

// registerAgentIP maps the launched container's bridge IP to its agent name so
// proxy connections attribute to the right run. Best-effort by contract: an
// inspect failure or an address-less container leaves accounting unattributed
// rather than failing the launch.
func (l dockerAgentLauncher) registerAgentIP(ctx context.Context, docker containerInspector, containerID, name string) {
	if l.agentIPs == nil || containerID == "" {
		return
	}
	inspect, err := docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return
	}
	l.agentIPs.Register(inspect.IPAddress, name)
}

// translateLaunchErr bridges the agent package's single-flight sentinel to the
// daemon's AgentLauncher-contract sentinel. This is the one place that imports
// both packages, so the translation lives here rather than in either package
// (agent imports daemon — the reverse would cycle). A benign already-running
// refusal becomes daemon.ErrAgentAlreadyRunning so the board launcher swallows
// it; every other error (and nil) passes through unchanged (SC-1419).
func translateLaunchErr(err error) error {
	if err == nil {
		return nil
	}
	if goerrors.Is(err, agent.ErrAlreadyRunning) {
		return daemon.ErrAgentAlreadyRunning
	}
	return err
}

// boardProjectDir resolves the single registered project's directory, the repo
// the board's git probes run against. ok is false when no project is registered,
// or when more than one is: these probes carry no card key to route by, so a
// multi-project daemon skips them rather than guessing a fixed project (SC-1694).
func boardProjectDir(projects *daemon.ProjectRegistry) (string, bool) {
	if projects == nil {
		return "", false
	}
	entry, err := projects.SoleEntry()
	if err != nil {
		return "", false
	}
	return entry.Dir, true
}

// boardParticipation returns the predicate the reconcile gate consults to decide
// whether THIS machine should drive a card's project at all. It routes the card's
// PM key to its registered project and reads that project's "board.participate"
// flag, which defaults to true — so a machine that configures nothing keeps
// today's behaviour and only an explicit opt-out stands a registered project down
// (SC-2047 opt-in participation). A key that cannot be routed to a single project
// (unrecorded, or ambiguous across projects) is treated as NOT this machine's to
// drive: acting on work it cannot even attribute to one project is exactly the
// cross-project overreach the opt-in is meant to prevent.
func boardParticipation(projects *daemon.ProjectRegistry) daemon.ProjectParticipation {
	if projects == nil {
		return nil
	}
	return func(pmKey string) bool {
		entry, err := projects.EntryForKey(pmKey)
		if err != nil {
			return false
		}
		return config.BoardParticipates(entry.Dir)
	}
}

// boardBranchReachable reports whether a handoff branch resolves on this machine
// (local ref or origin) — a board-context fix leaves its branch local on the
// machine that produced it (SC-652). A 15s timeout bounds the git probe. The
// tri-state result distinguishes a branch this machine confirmed absent from a
// probe that could not run at all (unresolvable project dir, git error,
// timeout) — the latter must never be read as evidence the branch is missing
// (SC-2403).
func boardBranchReachable(ctx context.Context, projects *daemon.ProjectRegistry, branch string) daemon.ProbeResult {
	dir, ok := boardProjectDir(projects)
	if !ok {
		return daemon.ProbeResult{Status: daemon.ProbeUnreadable,
			Detail: "the board's project directory could not be resolved (no single registered project) — run the check on the machine that owns the project"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	switch gitrepo.BranchReachability(probeCtx, dir, branch) {
	case gitrepo.ReachabilityPresent:
		return daemon.ProbeResult{Status: daemon.ProbePresent}
	case gitrepo.ReachabilityAbsent:
		return daemon.ProbeResult{Status: daemon.ProbeAbsent}
	default:
		return daemon.ProbeResult{Status: daemon.ProbeUnreadable,
			Detail: "the branch-reachability git probe against " + dir + " errored or timed out — retry when the repository is reachable"}
	}
}

// boardCommitsPresent reports whether every named commit is reachable from
// branch on this machine — the gate that keeps a handoff from naming SHAs no
// machine could see (735). A definite absence or an unreadable probe (dir
// unresolved, git error, timeout) short-circuits the loop; only a probe that
// confirmed every commit reads as Present, and only a probe that could not
// complete reads as Unreadable rather than as a phantom-commit absence
// (SC-2403).
func boardCommitsPresent(ctx context.Context, projects *daemon.ProjectRegistry, branch string, commits []string) daemon.ProbeResult {
	dir, ok := boardProjectDir(projects)
	if !ok {
		return daemon.ProbeResult{Status: daemon.ProbeUnreadable,
			Detail: "the board's project directory could not be resolved (no single registered project) — run the check on the machine that owns the project"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for _, sha := range commits {
		switch gitrepo.CommitReachability(probeCtx, dir, branch, sha) {
		case gitrepo.ReachabilityAbsent:
			return daemon.ProbeResult{Status: daemon.ProbeAbsent}
		case gitrepo.ReachabilityUnknown:
			return daemon.ProbeResult{Status: daemon.ProbeUnreadable,
				Detail: "the commit-presence git probe against " + dir + " errored or timed out for " + sha + " — retry when the repository is reachable"}
		}
	}
	return daemon.ProbeResult{Status: daemon.ProbePresent}
}

// forgeDeployer implements daemon.Deployer: push + PR, the CI gate, the merge
// and branch cleanup, all on the workspace's forge. It resolves the forge by
// role/kind from the configured instances rather than by key prefix, per call,
// so a config change takes effect without a daemon restart.
type forgeDeployer struct {
	resolver *vault.Resolver
	lookup   config.EnvLookup
}

func (p forgeDeployer) PushAndCreatePR(ctx context.Context, req daemon.PRRequest) (daemon.PRResult, error) {
	// Push first: a failed push must surface as deploy-failed BEFORE any PR is
	// opened, so we never leave a half-created PR pointing at an unpushed branch.
	// When the branch already exists on origin (a re-push after a rebase/retry) a
	// plain push is rejected on a diverged tip, so lease-push against the recorded
	// remote SHA — advancing the remote without clobbering a concurrent push (735).
	if err := p.pushBranch(ctx, req.WorkspaceDir, req.Branch); err != nil {
		return daemon.PRResult{}, err
	}

	creator, repo, err := resolveForge(req.WorkspaceDir, p.lookup, p.resolver)
	if err != nil {
		return daemon.PRResult{}, err
	}

	base := gitrepo.DefaultBranch(ctx, req.WorkspaceDir)
	pr, err := forge.AdoptOrCreatePullRequest(ctx, creator, &forge.PullRequest{
		Repo:  repo,
		Base:  base,
		Head:  req.Branch,
		Title: req.Title,
		Body:  req.Body,
		Draft: req.Draft,
	})
	if err != nil {
		return daemon.PRResult{}, errors.WrapWithDetails(err, "opening pull request", "repo", repo, "head", req.Branch)
	}
	return daemon.PRResult{URL: pr.URL, Number: pr.Number}, nil
}

// pushBranch pushes branch to origin, lease-pushing against the current remote
// tip when the branch already exists there (a re-push after a rebase) and plain-
// pushing a brand-new branch. A lease push advances a diverged remote without
// overwriting a concurrent push; a plain push of a fresh branch has no remote tip
// to lease against.
func (p forgeDeployer) pushBranch(ctx context.Context, dir, branch string) error {
	if !gitrepo.BranchExistsRemote(ctx, dir, branch) {
		return gitrepo.Push(ctx, dir, branch)
	}
	sourceSHA, err := gitrepo.RevParse(ctx, dir, branch)
	if err != nil {
		return err
	}
	if err := refuseIfBehind(ctx, dir, branch, sourceSHA); err != nil {
		return err
	}
	remoteSHA, err := gitrepo.RevParse(ctx, dir, "origin/"+branch)
	if err != nil {
		return err
	}
	return gitrepo.PushWithLease(ctx, dir, branch, remoteSHA)
}

// refuseIfBehind is the never-publish-behind-origin guard shared by every deploy
// publish site. --force-with-lease guards a DIVERGED remote but not a source
// that is strictly BEHIND origin, so without this a frozen or stale local tip
// silently overwrites newer origin work — the exact data loss SC-2322 exposes.
// It fetches origin/<branch> fresh, then refuses ONLY when sourceSHA is strictly
// behind origin (an ancestor of, and not equal to, the origin tip); the equal
// case proceeds and the ahead/diverged case is left to the existing lease. The
// refusal error's MESSAGE (not just its structured details) names the commits
// that would be lost, because errors.CauseChain renders err.Error() text — so
// the deploy-failed marker can point the reader at what to recover.
func refuseIfBehind(ctx context.Context, dir, branch, sourceSHA string) error {
	if err := gitrepo.Fetch(ctx, dir, branch); err != nil {
		return err
	}
	originSHA, err := gitrepo.RevParse(ctx, dir, "origin/"+branch)
	if err != nil {
		return err
	}
	if sourceSHA == originSHA {
		return nil
	}
	if !gitrepo.IsAncestor(ctx, dir, sourceSHA, originSHA) {
		return nil // ahead or diverged — not behind, not this guard's concern
	}
	lost, err := gitrepo.CommitsBetween(ctx, dir, sourceSHA, originSHA)
	if err != nil {
		return err
	}
	return errors.WithDetails(
		"refusing to publish %s: the source is behind origin and would overwrite %d newer commit(s) that must survive:\n%s",
		"branch", branch,
		"lost", len(lost),
		"commits", describeCommits(lost),
		"local", sourceSHA,
		"origin", originSHA,
	)
}

// describeCommits renders one indented "<short> <subject>" line per commit, so
// a behind-publish refusal names exactly the newer work it protected.
func describeCommits(commits []gitrepo.Commit) string {
	var b strings.Builder
	for _, c := range commits {
		b.WriteString("  ")
		b.WriteString(c.ShortSHA)
		b.WriteString(" ")
		b.WriteString(c.Subject)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// EnsureMergeable makes the handoff branch current with the base before the
// deploy attempts the merge: it fetches the base, and when the branch does not
// already contain the base tip it rebases the branch onto origin/<base>,
// re-pushes (lease when the branch is on origin), and re-verifies. The rebase
// runs in an ephemeral detached worktree, never in the live workspace checkout:
// git refuses to rebase in a dirty worktree, so ANY uncommitted user change
// would fail every deploy — and a rebase that did run would check the handoff
// branch out under the user (SC-1000). A rebase error is a real conflict the
// mechanical path cannot resolve — the deploy must fail loudly rather than
// merge blind (735).
func (p forgeDeployer) EnsureMergeable(ctx context.Context, req daemon.PRRequest) (bool, error) {
	dir, branch := req.WorkspaceDir, req.Branch
	base := gitrepo.DefaultBranch(ctx, dir)
	if err := gitrepo.Fetch(ctx, dir, base); err != nil {
		return false, err
	}
	originBase := "origin/" + base
	tip, onOrigin, err := branchTip(ctx, dir, branch)
	if err != nil {
		return false, err
	}
	// Already current: the branch contains the base tip, so its PR is mergeable
	// without touching it.
	if gitrepo.IsAncestor(ctx, dir, originBase, tip) {
		return false, nil
	}
	wt, cleanup, err := addEphemeralWorktree(ctx, dir, tip)
	if err != nil {
		return false, err
	}
	defer cleanup()
	id, loadErr := botidentity.Load(dir)
	if loadErr != nil {
		id = botidentity.Identity{Name: botidentity.DefaultName, Email: botidentity.DefaultEmail}
	}
	if err := gitrepo.RebaseHead(ctx, wt, originBase, id.Name, id.Email); err != nil {
		return false, err
	}
	newTip, err := gitrepo.RevParse(ctx, wt, "HEAD")
	if err != nil {
		return false, err
	}
	// The worktree rebased a detached HEAD, so publishing is a refspec push of
	// the worktree's HEAD — the branch itself is never checked out anywhere.
	// Lease against the recorded pre-rebase tip when the branch is on origin so
	// a concurrent push is refused, not clobbered.
	if onOrigin {
		// Same never-publish-behind-origin invariant as pushBranch: refuse before
		// the lease push if the rebased tip is strictly behind origin (SC-2322).
		if err := refuseIfBehind(ctx, dir, branch, newTip); err != nil {
			return false, err
		}
		if err := gitrepo.PushHeadWithLease(ctx, wt, branch, tip); err != nil {
			return false, err
		}
	} else if err := gitrepo.PushHead(ctx, wt, branch); err != nil {
		return false, err
	}
	// A clean rebase that still does not contain the base tip means the branch
	// could not be made mergeable — surface it rather than merge into a conflict.
	if !gitrepo.IsAncestor(ctx, dir, originBase, newTip) {
		return true, errors.WithDetails("branch still not mergeable after rebase", "branch", branch, "base", base)
	}
	return true, nil
}

// PublishResolvedBranch carries a deploy-fixer's conflict resolution from the
// local branch ref to origin. The fixer runs in a board container, which holds
// no push credentials by design — the daemon publishes on its behalf, the same
// division of labour the pr-fixer already relies on. Without this the resolution
// is unreachable: branchTip below reads the branch from origin, so the deploy
// would rebase the unresolved origin tip again and hit the identical conflict,
// discarding the finished work sitting on the local ref (SC-2845).
//
// It publishes ONLY a ref that is genuinely a resolution — present locally,
// different from the origin tip, and already containing the base tip (the
// property the fixer was dispatched to establish). Anything else is left
// untouched for EnsureMergeable's own freshness rebase to handle, so a fixer
// that reported done without moving the branch cannot make the deploy publish
// something unexamined. The publish itself goes through pushBranch, inheriting
// the never-publish-behind-origin guard and the lease against a concurrent push.
func (p forgeDeployer) PublishResolvedBranch(ctx context.Context, dir, branch string) (bool, error) {
	if !gitrepo.BranchExistsLocal(ctx, dir, branch) {
		return false, nil
	}
	base := gitrepo.DefaultBranch(ctx, dir)
	if err := gitrepo.Fetch(ctx, dir, base); err != nil {
		return false, err
	}
	local, err := gitrepo.RevParse(ctx, dir, branch)
	if err != nil {
		return false, err
	}
	// Not yet current with the base: whatever the fixer did, it is not the
	// resolution the deploy is waiting for.
	if !gitrepo.IsAncestor(ctx, dir, "origin/"+base, local) {
		return false, nil
	}
	if gitrepo.BranchExistsRemote(ctx, dir, branch) {
		if err := gitrepo.Fetch(ctx, dir, branch); err != nil {
			return false, err
		}
		remote, err := gitrepo.RevParse(ctx, dir, "origin/"+branch)
		if err != nil {
			return false, err
		}
		if local == remote {
			return false, nil // already published — nothing to carry
		}
	}
	if err := p.pushBranch(ctx, dir, branch); err != nil {
		return false, err
	}
	return true, nil
}

// branchTip resolves the commit the freshness rebase starts from, preferring
// the origin ref: the deploy serves the PR (which lives on origin), and the
// local ref may lag a prior deploy's rebase — the ephemeral-worktree rebase
// publishes to origin without rewriting local branches. The origin ref is
// fetched first so the recorded tip is the actual remote state, not a stale
// tracking ref.
func branchTip(ctx context.Context, dir, branch string) (sha string, onOrigin bool, err error) {
	if gitrepo.BranchExistsRemote(ctx, dir, branch) {
		if err := gitrepo.Fetch(ctx, dir, branch); err != nil {
			return "", true, err
		}
		sha, err := gitrepo.RevParse(ctx, dir, "origin/"+branch)
		return sha, true, err
	}
	sha, err = gitrepo.RevParse(ctx, dir, branch)
	return sha, false, err
}

// addEphemeralWorktree creates a throwaway detached worktree at tip, sharing
// dir's object DB, and returns its path plus a cleanup that removes it. The
// rebase result's objects survive removal in the shared DB.
func addEphemeralWorktree(ctx context.Context, dir, tip string) (string, func(), error) {
	wt, err := os.MkdirTemp("", "human-deploy-rebase-")
	if err != nil {
		return "", nil, errors.WrapWithDetails(err, "creating ephemeral rebase worktree dir")
	}
	if err := gitrepo.WorktreeAdd(ctx, dir, wt, tip); err != nil {
		_ = os.RemoveAll(wt)
		return "", nil, err
	}
	return wt, func() { _ = gitrepo.WorktreeRemove(ctx, dir, wt) }, nil
}

// MarkReadyForReview un-drafts the review loop's PR so the adopted PR can merge
// once the machine review approves. Mirrors the resolveForge + capability
// type-assert shape of the other forgeDeployer forge operations.
func (p forgeDeployer) MarkReadyForReview(ctx context.Context, workspaceDir string, number int) error {
	creator, repo, err := resolveForge(workspaceDir, p.lookup, p.resolver)
	if err != nil {
		return err
	}
	marker, ok := creator.(forge.ReadyForReviewMarker)
	if !ok {
		return errors.WithDetails("forge does not support marking a PR ready for review", "repo", repo)
	}
	return marker.MarkReadyForReview(ctx, repo, number)
}

func (p forgeDeployer) PullRequestChecks(ctx context.Context, workspaceDir string, number int) (forge.ChecksState, error) {
	creator, repo, err := resolveForge(workspaceDir, p.lookup, p.resolver)
	if err != nil {
		return "", err
	}
	checker, ok := creator.(forge.ChecksReader)
	if !ok {
		return "", errors.WithDetails("forge does not support reading CI checks", "repo", repo)
	}
	return checker.PullRequestChecks(ctx, repo, number)
}

func (p forgeDeployer) ReadPullRequest(ctx context.Context, workspaceDir string, number int) (*forge.PullRequestState, error) {
	creator, repo, err := resolveForge(workspaceDir, p.lookup, p.resolver)
	if err != nil {
		return nil, err
	}
	reader, ok := creator.(forge.PullRequestReader)
	if !ok {
		return nil, errors.WithDetails("forge does not support reading pull request state", "repo", repo)
	}
	return reader.ReadPullRequest(ctx, repo, number)
}

func (p forgeDeployer) PullRequestMergeable(ctx context.Context, workspaceDir string, number int) (bool, error) {
	creator, repo, err := resolveForge(workspaceDir, p.lookup, p.resolver)
	if err != nil {
		return false, err
	}
	reader, ok := creator.(forge.MergeReader)
	if !ok {
		return false, errors.WithDetails("forge does not support reading mergeability", "repo", repo)
	}
	return reader.PullRequestMergeable(ctx, repo, number)
}

func (p forgeDeployer) MergePullRequest(ctx context.Context, workspaceDir string, number int) error {
	creator, repo, err := resolveForge(workspaceDir, p.lookup, p.resolver)
	if err != nil {
		return err
	}
	merger, ok := creator.(forge.Merger)
	if !ok {
		return errors.WithDetails("forge does not support merging pull requests", "repo", repo)
	}
	return merger.MergePullRequest(ctx, repo, number)
}

func (p forgeDeployer) DeleteRemoteBranch(ctx context.Context, workspaceDir, branch string) error {
	creator, repo, err := resolveForge(workspaceDir, p.lookup, p.resolver)
	if err != nil {
		return err
	}
	deleter, ok := creator.(forge.BranchDeleter)
	if !ok {
		return errors.WithDetails("forge does not support deleting branches", "repo", repo)
	}
	return deleter.DeleteBranch(ctx, repo, branch)
}

// BranchMerged reports whether the branch is already contained in the base
// branch. It fetches the base first (like EnsureMergeable) so the ancestor test
// runs against the current remote tip, then checks whether branch is an ancestor
// of origin/<base>. A fetch error returns false — fall through to the normal
// deploy path rather than skip a ship on a transient network blip (SC-911).
func (p forgeDeployer) BranchMerged(ctx context.Context, workspaceDir, branch string) bool {
	base := gitrepo.DefaultBranch(ctx, workspaceDir)
	if err := gitrepo.Fetch(ctx, workspaceDir, base); err != nil {
		return false
	}
	return gitrepo.IsAncestor(ctx, workspaceDir, branch, "origin/"+base)
}

// resolveForge finds the workspace's configured code host and resolves the
// "owner/repo" from origin.
//
// It reads the forge list. The old version walked the tracker list looking for
// entries that happened to carry a forge client, with a kind test beside it
// doing the same job by another route — both artefacts of one list holding two
// kinds of thing ([SC-3876]).
func resolveForge(dir string, lookup config.EnvLookup, resolver *vault.Resolver) (forge.Creator, string, error) {
	forges, err := cmdutil.LoadAllForges(dir, lookup, resolver)
	if err != nil {
		return nil, "", err
	}
	if len(forges) == 0 {
		// The deploy path is where this failure lands for most people, so it
		// carries the same instructions as the CLI error rather than a terse
		// "not configured" that reads like a bug in the deployer.
		return nil, "", errors.WithDetails(forge.NoForgeConfigured("github"), "dir", dir)
	}
	creator := forges[0].Forge

	raw, err := gitrepo.OriginURL(context.Background(), dir)
	if err != nil {
		return nil, "", err
	}
	_, repo, ok := forge.ParseRemoteURL(raw)
	if !ok {
		return nil, "", errors.WithDetails("could not parse git origin remote", "remote", raw)
	}
	return creator, repo, nil
}

// FindOpenWorkForKey resolves the workspace forge and reports the open pull
// requests and branches referencing key — the authoritative "already underway"
// signal preflight consults (SC-2648). It shares resolveForge so the CLI check
// and the deploy path cannot drift in how they pick the forge. Returns the
// resolved "owner/repo" alongside the work so callers can name the repo.
func FindOpenWorkForKey(ctx context.Context, dir string, lookup config.EnvLookup, resolver *vault.Resolver, key string) ([]forge.OpenWork, string, error) {
	creator, repo, err := resolveForge(dir, lookup, resolver)
	if err != nil {
		return nil, "", err
	}
	finder, ok := creator.(forge.OpenWorkFinder)
	if !ok {
		return nil, repo, errors.WithDetails("forge does not support open-work lookup", "repo", repo)
	}
	work, err := finder.FindOpenWork(ctx, repo, key)
	return work, repo, err
}

// boardPRMerged reports whether the PR at prURL has been merged on the
// workspace's forge. A forge that cannot read merge status, or a URL with no
// parseable number, returns (false, err) so the reconcile pass leaves the card
// untouched rather than clearing a red on an unknown state (SC-910).
func boardPRMerged(ctx context.Context, projects *daemon.ProjectRegistry, resolver *vault.Resolver, prURL string) (bool, error) {
	entry, err := projects.SoleEntry()
	if err != nil {
		return false, err
	}
	number, ok := forge.PullRequestNumberFromURL(prURL)
	if !ok {
		return false, errors.WithDetails("could not parse pull request number", "pr", prURL)
	}
	creator, repo, err := resolveForge(entry.Dir, entry.EnvLookup(), resolver)
	if err != nil {
		return false, err
	}
	reader, ok := creator.(forge.MergedReader)
	if !ok {
		return false, errors.WithDetails("forge does not support reading merge status", "repo", repo)
	}
	return reader.PullRequestMerged(ctx, repo, number)
}

// resolvePMCommenter resolves the PM-role tracker.Commenter for a workspace.
// It selects by ROLE (InferRole()=="pm"), never by key prefix: both trackers
// can be configured with the same name, so key auto-detect mis-routes.
func resolvePMCommenter(dir string, lookup config.EnvLookup, resolver *vault.Resolver) (tracker.Commenter, error) {
	instances, err := cmdutil.LoadAllInstancesWithResolver(dir, lookup, resolver)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.InferRole() != "pm" {
			continue
		}
		if c, ok := inst.Provider.(tracker.Commenter); ok {
			return c, nil
		}
	}
	return nil, errors.WithDetails("no PM-role tracker with comment support configured", "dir", dir)
}

// resolvePMGetter resolves the PM-role tracker.Getter for a workspace.
// Role-based selection (InferRole()=="pm"), never key prefix — mirrors
// resolvePMCommenter. tracker.Provider embeds Getter, so the PM instance
// satisfies it.
func resolvePMGetter(dir string, lookup config.EnvLookup, resolver *vault.Resolver) (tracker.Getter, error) {
	instances, err := cmdutil.LoadAllInstancesWithResolver(dir, lookup, resolver)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.InferRole() != "pm" {
			continue
		}
		if g, ok := inst.Provider.(tracker.Getter); ok {
			return g, nil
		}
	}
	return nil, errors.WithDetails("no PM-role tracker with fetch support configured", "dir", dir)
}

// resolvePMCurrentUser resolves the PM-role tracker.CurrentUserNamer for a
// workspace. Role-based selection (InferRole()=="pm"), never key prefix —
// mirrors resolvePMGetter. Optional capability: a PM provider that does not
// implement it yields (nil, nil), and the caller reads that as "no identity".
func resolvePMCurrentUser(dir string, lookup config.EnvLookup, resolver *vault.Resolver) (tracker.CurrentUserNamer, error) {
	instances, err := cmdutil.LoadAllInstancesWithResolver(dir, lookup, resolver)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.InferRole() != "pm" {
			continue
		}
		if n, ok := inst.Provider.(tracker.CurrentUserNamer); ok {
			return n, nil
		}
	}
	return nil, nil
}

// currentUserFunc builds the daemon's current-user fetcher: the display name of
// the authenticated PM-tracker user, for the board's ownership dimming
// (SC-3339). Empty (no error) when no PM tracker resolves an identity, so the
// board simply dims nothing.
func currentUserFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) func() (string, error) {
	return func() (string, error) {
		for _, entry := range reg.Entries() {
			namer, err := resolvePMCurrentUser(entry.Dir, entry.EnvLookup(), resolver)
			if err != nil {
				return "", err
			}
			if namer == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			name, err := namer.CurrentUserName(ctx)
			cancel()
			if err != nil {
				return "", err
			}
			return name, nil
		}
		return "", nil
	}
}

// closedTicketProbeFunc builds the reconcile pass's ClosedTicketProbe: it fetches
// one PM ticket by key and reports whether its status has landed in the done or
// closed category — the same categories DeriveBoardCard hides the card on.
//
// It is asked only about a key no open card matched, so it stays off the hot
// path entirely on a healthy board. Every failure propagates as an error rather
// than a false: the caller must be able to tell "confirmed closed" from "could
// not tell", because only the former may stop a live run (1698).
func closedTicketProbeFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) daemon.ClosedTicketProbe {
	return func(ctx context.Context, pmKey string) (bool, error) {
		entry, err := reg.EntryForKey(pmKey)
		if err != nil {
			return false, err
		}
		getter, err := resolvePMGetter(entry.Dir, entry.EnvLookup(), resolver)
		if err != nil {
			return false, err
		}
		fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		issue, err := getter.GetIssue(fetchCtx, pmKey)
		if err != nil {
			return false, errors.WrapWithDetails(err, "fetching PM ticket to confirm it is closed", "pm", pmKey)
		}
		return issue.StatusType == tracker.CategoryDone || issue.StatusType == tracker.CategoryClosed, nil
	}
}

// blockedByProbeFunc builds the stage gate's dependency probe: it reads the
// links the tracker already records on pmKey and returns the blockers that have
// not finished.
//
// Resolving each blocker's real status here is what lets the gate stay simple —
// it never has to decide what "open" means, and a blocker that finished last
// week is simply absent. The extra fetches run once per launch attempt, not per
// render, and only for a ticket that actually carries links.
func blockedByProbeFunc(dir string, lookup config.EnvLookup, resolver *vault.Resolver, logger zerolog.Logger) func(context.Context, string) ([]string, error) {
	return func(ctx context.Context, pmKey string) ([]string, error) {
		getter, err := resolvePMGetter(dir, lookup, resolver)
		if err != nil {
			return nil, err
		}
		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		issue, err := getter.GetIssue(fetchCtx, pmKey)
		if err != nil {
			return nil, errors.WrapWithDetails(err, "reading a ticket's dependencies", "pm", pmKey)
		}
		return openBlockers(fetchCtx, getter, issue, logger), nil
	}
}

// openBlockers keeps the blockers of issue that have not finished.
//
// A blocker that cannot be read is kept. Failing to read it is not evidence
// that it finished, and the link is deliberate data: holding the work is the
// direction that cannot cause the collision the dependency was written to
// prevent. The refusal names the blocker, so this is visible rather than a
// silent stall.
func openBlockers(ctx context.Context, getter tracker.Getter, issue *tracker.Issue, logger zerolog.Logger) []string {
	open := make([]string, 0, len(issue.Links))
	for _, key := range issue.BlockedBy() {
		blocker, err := getter.GetIssue(ctx, key)
		if err != nil {
			logger.Warn().Err(err).Str("pm", issue.Key).Str("blocker", key).
				Msg("board gate: cannot read a blocker, treating it as unfinished")
			open = append(open, key)
			continue
		}
		if blocker.StatusType == tracker.CategoryDone || blocker.StatusType == tracker.CategoryClosed {
			continue
		}
		open = append(open, key)
	}
	return open
}

// resolvePMTransitioner resolves the PM-role tracker.Transitioner for a
// workspace. Role-based selection (InferRole()=="pm"), never key prefix —
// mirrors resolvePMCommenter. tracker.Provider embeds Transitioner, so the PM
// instance satisfies it.
func resolvePMTransitioner(dir string, lookup config.EnvLookup, resolver *vault.Resolver) (tracker.Transitioner, error) {
	instances, err := cmdutil.LoadAllInstancesWithResolver(dir, lookup, resolver)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.InferRole() != "pm" {
			continue
		}
		if t, ok := inst.Provider.(tracker.Transitioner); ok {
			return t, nil
		}
	}
	return nil, errors.WithDetails("no PM-role tracker with transition support configured", "dir", dir)
}

// resolvePMOwner resolves the PM-role tracker as the provider that can record
// ownership. It returns the provider itself rather than a narrowed interface
// because claiming needs two capabilities together — resolving the current user
// and assigning — and tracker.AssignToCurrentUser type-asserts for both.
func resolvePMOwner(dir string, lookup config.EnvLookup, resolver *vault.Resolver) (tracker.Provider, error) {
	instances, err := cmdutil.LoadAllInstancesWithResolver(dir, lookup, resolver)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.InferRole() != "pm" {
			continue
		}
		return inst.Provider, nil
	}
	return nil, errors.WithDetails("no PM-role tracker configured to record ownership", "dir", dir)
}

// closeTicketerFunc builds the daemon's CloseTicketer closure: it stops any live
// run claiming the ticket, then resolves the PM transitioner by role per request
// and moves the ticket to its Done status. "done" is the status CATEGORY, not a
// literal label — the tracker resolves it to the workflow's done state, the same
// convention `issue start` uses with "started", so no team-specific status name
// is hardcoded.
//
// Closing IS cancellation (1698): the board close reads to the user as "stop
// this", so it must actually stop the claiming agent and release its container
// before the ticket leaves the board — otherwise the run keeps working invisibly
// against a closed card. The transition is GATED on a confirmed stop: if the run
// could not be stopped (or could not even be enumerated to stop it), the ticket
// is left open — and thus reachable by the reconcile safety net, which lists open
// cards only — rather than closing over a run that refused to die.
func closeTicketerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, liveAgents func() ([]string, error), stopAgent func(context.Context, string) error, logger zerolog.Logger) func(daemon.CloseTicketRequest) error {
	return func(req daemon.CloseTicketRequest) error {
		entry, err := reg.EntryForKey(req.PMKey)
		if err != nil {
			return err
		}
		lookup := entry.EnvLookup()

		stopCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		_, stopErr := daemon.StopAgentsForPMKey(stopCtx, req.PMKey, liveAgents, stopAgent, logger)
		cancel()
		if stopErr != nil {
			return stopErr
		}

		transitioner, err := resolvePMTransitioner(entry.Dir, lookup, resolver)
		if err != nil {
			return err
		}
		return transitioner.TransitionIssue(context.Background(), req.PMKey, "done")
	}
}

// advancePRLoopFunc builds the PR review→fix loop's Stop-event driver: on each
// reviewer/fixer exit it reads the outcome that step recorded in the state store
// (the reviewer's verdict, the fixer's exit) and hands it to the loop executor,
// which decides and runs the next step. Extracted from runDaemonForeground so
// the state-read + error path lives in its own scope.
// The exiting run's name and error type travel with the call so a step that died
// before recording an outcome can still be explained from its artifacts; both are
// empty when the durable reconcile pass re-drives a stalled loop, where the agent
// is long gone (SC-1892).
func advancePRLoopFunc(ctx context.Context, ds *daemonState, diagnose daemon.BoardFailureDiagnoser, reviewLaunchGate func(context.Context) []daemon.DoctorCheck, logger zerolog.Logger) func(pmKey, agentName, errorType string) error {
	return func(pmKey, agentName, errorType string) error {
		deps, err := boardTransitionDepsFor(ds.srv.Projects, pmKey, ds.vaultResolver, ds.daemonID, logger, reviewLaunchGate, ds.agentIPs)
		if err != nil {
			return err
		}
		deps.Diagnose = diagnose

		// The identity anchor for the reads below (SC-2378/AD2): the round's own
		// started-marker time. A state-store write timestamped before it can only
		// be a previous round's leftover, never this round's outcome. Comments are
		// listed once here and reused for both anchors rather than re-listed per
		// read.
		project := boardStateProject(ds.srv.Projects, pmKey)
		var reviewAnchor, fixAnchor time.Time
		if comments, cerr := deps.Commenter.ListComments(ctx, pmKey); cerr == nil {
			reviewAnchor, _ = daemon.LatestMarkerTime(comments, daemon.PRReviewStartedHeader)
			fixAnchor, _ = daemon.LatestMarkerTime(comments, daemon.PRFixStartedHeader)
		}

		verdict, reviewHead, verdictRecorded, verdictFresh := readPRReviewVerdict(ctx, project, pmKey, reviewAnchor, logger)
		exit, options, summary, fixHead, exitRecorded, exitFresh := readPRFixReport(ctx, project, pmKey, fixAnchor, logger)
		return deps.AdvancePRLoop(ctx, pmKey, daemon.PRLoopOutcome{
			ReviewVerdict:  verdict,
			ReviewRecorded: verdictRecorded,
			ReviewHead:     reviewHead,
			ReviewStale:    verdictRecorded && !verdictFresh,
			FixExit:        exit,
			FixRecorded:    exitRecorded,
			FixHead:        fixHead,
			FixStale:       exitRecorded && !exitFresh,
			FixOptions:     options,
			FixSummary:     summary,
			Agent:          agentName,
			ErrorType:      errorType,
		})
	}
}

// advanceDeployFixFunc builds the deploy-fixer's Stop-event driver: on the fixer's
// exit it reads the exit recorded in stage.deploy-fix and hands it to the deploy-fix
// executor, which re-runs Deploy on `done` or reds the card on anything else.
func advanceDeployFixFunc(ctx context.Context, ds *daemonState, launchGate func(context.Context) []daemon.DoctorCheck, logger zerolog.Logger) func(pmKey string) error {
	return func(pmKey string) error {
		deps, err := boardTransitionDepsFor(ds.srv.Projects, pmKey, ds.vaultResolver, ds.daemonID, logger, launchGate, ds.agentIPs)
		if err != nil {
			return err
		}
		var anchor time.Time
		if comments, cerr := deps.Commenter.ListComments(ctx, pmKey); cerr == nil {
			anchor, _ = daemon.LatestMarkerTime(comments, daemon.DeployFixStartedHeader)
		}
		exit := readDeployFixExit(ctx, boardStateProject(ds.srv.Projects, pmKey), pmKey, anchor, logger)
		return deps.AdvanceDeployFix(ctx, pmKey, exit)
	}
}

// boardStateProject resolves the project a board-driven state read/write for
// pmKey belongs to, so the daemon's own driver reads back exactly what an
// agent working that ticket wrote — the same routing HUMAN_STATE_PROJECT
// gives a forwarded "human state" command (SC-2326). Fewer than two
// registered projects, or a key the registry cannot place, resolve to the
// default project "" rather than guessing.
func boardStateProject(reg *daemon.ProjectRegistry, pmKey string) string {
	if reg == nil || len(reg.Entries()) < 2 {
		return ""
	}
	entry, err := reg.EntryForKey(pmKey)
	if err != nil {
		return ""
	}
	return entry.Name
}

// boardTransitionDepsFor resolves the transition engine's collaborators for
// the single registered project: the PM commenter by role, the Docker launcher
// and the forge publisher against the resolved project dir. Shared by the
// board-transition and board-fix closures so both routes drive the exact same
// engine.
func boardTransitionDepsFor(reg *daemon.ProjectRegistry, pmKey string, resolver *vault.Resolver, daemonID string, logger zerolog.Logger, launchGate func(context.Context) []daemon.DoctorCheck, agentIPs *daemon.AgentIPRegistry) (daemon.BoardTransitionDeps, error) {
	entry, err := reg.EntryForKey(pmKey)
	if err != nil {
		return daemon.BoardTransitionDeps{}, err
	}
	lookup := entry.EnvLookup()
	commenter, err := resolvePMCommenter(entry.Dir, lookup, resolver)
	if err != nil {
		return daemon.BoardTransitionDeps{}, err
	}
	// The Getter lets a recovery relaunch classify the ticket as a self-planning
	// fix pipeline; best-effort, since a tracker without fetch support simply
	// leaves classification to the marker heuristic (SC-2986).
	getter, gErr := resolvePMGetter(entry.Dir, lookup, resolver)
	if gErr != nil {
		logger.Debug().Err(gErr).Str("pm", pmKey).
			Msg("board transition: no PM getter; relaunch will use the marker heuristic")
		getter = nil
	}
	// Sign every internal board post at this single choke point: the daemon id is
	// the machine, BuildRevision the build, so d.Commenter carries provenance
	// without any per-writer stamping call.
	commenter = marker.NewSigningCommenter(commenter, daemonID, daemon.BuildRevision())
	return daemon.BoardTransitionDeps{
		Commenter: commenter,
		Getter:    getter,
		Launcher:  dockerAgentLauncher{daemonID: daemonID, agentIPs: agentIPs},
		Deployer:  forgeDeployer{resolver: resolver, lookup: lookup},
		CloseTicket: func(pmKey string) error {
			transitioner, err := resolvePMTransitioner(entry.Dir, lookup, resolver)
			if err != nil {
				return err
			}
			return transitioner.TransitionIssue(context.Background(), pmKey, "done")
		},
		SetTicketOwner: func(pmKey string) error {
			owner, err := resolvePMOwner(entry.Dir, lookup, resolver)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return tracker.AssignToCurrentUser(ctx, owner, pmKey)
		},
		WorkspaceDir: entry.Dir,
		ConfigDir:    entry.Dir,
		DaemonID:     daemonID,
		Logger:       logger,
		LaunchGate:   launchGate,
		BlockedBy:    blockedByProbeFunc(entry.Dir, lookup, resolver, logger),
	}, nil
}

// boardTransitionerFunc builds the daemon's BoardTransitioner closure: it
// resolves the PM commenter by role per request and applies the transition with
// the Docker launcher and forge publisher against the resolved project dir.
func boardTransitionerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string, logger zerolog.Logger, launchGate func(context.Context) []daemon.DoctorCheck, agentIPs *daemon.AgentIPRegistry) func(daemon.BoardTransitionRequest) error {
	return func(req daemon.BoardTransitionRequest) error {
		deps, err := boardTransitionDepsFor(reg, req.PMKey, resolver, daemonID, logger, launchGate, agentIPs)
		if err != nil {
			return err
		}
		return deps.ApplyTransition(context.Background(), req)
	}
}

// boardRetryTransitionerFunc is boardTransitionerFunc's retry twin: it reports
// whether the relaunch actually LAUNCHED, so the retry accounting never charges an
// attempt for a refusal that started nothing (SC-2989).
func boardRetryTransitionerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string, logger zerolog.Logger, launchGate func(context.Context) []daemon.DoctorCheck, agentIPs *daemon.AgentIPRegistry) func(daemon.BoardTransitionRequest) (bool, error) {
	return func(req daemon.BoardTransitionRequest) (bool, error) {
		deps, err := boardTransitionDepsFor(reg, req.PMKey, resolver, daemonID, logger, launchGate, agentIPs)
		if err != nil {
			return false, err
		}
		return deps.ApplyRetryTransition(context.Background(), req)
	}
}

// boardFixerFunc builds the daemon's BoardFixer closure: same collaborators as
// a board transition, but the entry point is the autonomous bug-fix pipeline
// (planning gate skipped — autofix triages, plans and fixes in one run).
// boardOptionerFunc builds the daemon's BoardOptioner closure: it records a
// chosen option and relaunches the block's stage with the choice injected.
func boardOptionerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string, logger zerolog.Logger, launchGate func(context.Context) []daemon.DoctorCheck, agentIPs *daemon.AgentIPRegistry) func(daemon.BoardOptionRequest) error {
	return func(req daemon.BoardOptionRequest) error {
		deps, err := boardTransitionDepsFor(reg, req.PMKey, resolver, daemonID, logger, launchGate, agentIPs)
		if err != nil {
			return err
		}
		return deps.ApplyOption(context.Background(), req)
	}
}

func boardFixerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string, logger zerolog.Logger, launchGate func(context.Context) []daemon.DoctorCheck, agentIPs *daemon.AgentIPRegistry) func(daemon.BoardFixRequest) error {
	return func(req daemon.BoardFixRequest) error {
		deps, err := boardTransitionDepsFor(reg, req.PMKey, resolver, daemonID, logger, launchGate, agentIPs)
		if err != nil {
			return err
		}
		return deps.ApplyFix(context.Background(), req)
	}
}

// securityFixerFunc builds the daemon's BoardSecurityFixer closure: identical
// collaborators to a bug fix, but the entry point is the security-fix pipeline
// (/human-security-fix) — a security-tuned triage/verify pass over the same
// containerized agent path.
func securityFixerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string, logger zerolog.Logger, launchGate func(context.Context) []daemon.DoctorCheck, agentIPs *daemon.AgentIPRegistry) func(daemon.SecurityFixRequest) error {
	return func(req daemon.SecurityFixRequest) error {
		deps, err := boardTransitionDepsFor(reg, req.PMKey, resolver, daemonID, logger, launchGate, agentIPs)
		if err != nil {
			return err
		}
		return deps.ApplySecurityFix(context.Background(), req)
	}
}

// bugCreatorFunc builds the daemon's BugCreator closure: it files a bug-typed
// ticket on the role-resolved PM tracker. The provider maps the bug type onto
// its native defect marker (issue/story type where one exists, the bug label
// otherwise), so the Bugs pane recognises the card on every backend.
func bugCreatorFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, relate func(daemon.RelateRequest) error) func(daemon.BugCreateRequest) (daemon.BugCreateResponse, error) {
	return func(req daemon.BugCreateRequest) (daemon.BugCreateResponse, error) {
		if err := daemon.ValidateBugCreate(req); err != nil {
			return daemon.BugCreateResponse{}, err
		}
		entry, err := reg.SoleEntry()
		if err != nil {
			return daemon.BugCreateResponse{}, err
		}
		creator, project, err := resolvePMCreator(entry.Dir, entry.EnvLookup(), resolver)
		if err != nil {
			return daemon.BugCreateResponse{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		created, err := creator.CreateIssue(ctx, &tracker.Issue{
			Project:     project,
			Title:       req.Title,
			Description: req.Description,
			Type:        "Bug",
		})
		if err != nil {
			return daemon.BugCreateResponse{}, errors.WrapWithDetails(err, "creating bug ticket", "project", project)
		}
		// A filed bug is owned by whoever filed it (SC-3345), best-effort so a
		// failed claim never turns a created ticket into a failed filing.
		_, _ = tracker.AssignToCurrentUserBestEffort(ctx, creator, created.Key)
		// Filing returns immediately; the related-work record lands shortly after.
		// Fire-and-forget on a background context so a slow container start never
		// delays the caller. Only the interactive filing paths (the board Bugs pane
		// and `human bug create`) reach this closure — the findbugs/security sweeps
		// file with the raw provider create command and are excluded by
		// construction, satisfying "no automatic runs for sweep-filed bugs"
		// (SC-2405). A nil launcher (relate disabled) simply skips the triage.
		if relate != nil {
			go func(key, title string) {
				_ = relate(daemon.RelateRequest{PMKey: key, PMTitle: title})
			}(created.Key, req.Title)
		}
		return daemon.BugCreateResponse{Key: created.Key, URL: created.URL}, nil
	}
}

// securityCreatorFunc builds the daemon's SecurityCreator closure: it files a
// security ticket on the role-resolved PM tracker. Unlike a bug — which every
// backend marks with a native defect type — no tracker has a universal native
// "security" type, so the ticket carries the SecurityLabel explicitly (which
// every provider passes straight through on create). tracker.Issue.IsSecurity
// then recognises the card on every backend via that label, and the Security
// type is set for display where a tracker shows it.
func securityCreatorFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) func(daemon.SecurityCreateRequest) (daemon.SecurityCreateResponse, error) {
	return func(req daemon.SecurityCreateRequest) (daemon.SecurityCreateResponse, error) {
		if err := daemon.ValidateSecurityCreate(req); err != nil {
			return daemon.SecurityCreateResponse{}, err
		}
		entry, err := reg.SoleEntry()
		if err != nil {
			return daemon.SecurityCreateResponse{}, err
		}
		creator, project, err := resolvePMCreator(entry.Dir, entry.EnvLookup(), resolver)
		if err != nil {
			return daemon.SecurityCreateResponse{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		created, err := creator.CreateIssue(ctx, &tracker.Issue{
			Project:     project,
			Title:       req.Title,
			Description: req.Description,
			Type:        "Security",
			Labels:      []string{tracker.SecurityLabel},
		})
		if err != nil {
			return daemon.SecurityCreateResponse{}, errors.WrapWithDetails(err, "creating security ticket", "project", project)
		}
		// Owned by the reporter, like every other filing path (SC-3345).
		_, _ = tracker.AssignToCurrentUserBestEffort(ctx, creator, created.Key)
		return daemon.SecurityCreateResponse{Key: created.Key, URL: created.URL}, nil
	}
}

// relateLauncherFunc builds the daemon's RelateLauncher closure: it launches
// the /human-relate triage for one bug in the registered project's
// devcontainer, exactly like a board on-demand agent. It tears down any prior
// relate agent for the same key first so a re-run (or a manual run after an
// incomplete one) is idempotent — Manager.Start refuses to start over a live
// agent of the same name. The daemonID is stamped so a peer daemon spares the
// launch it did not start, like every other board-launched agent (SC-2405).
func relateLauncherFunc(reg *daemon.ProjectRegistry, daemonID string) func(daemon.RelateRequest) error {
	return func(req daemon.RelateRequest) error {
		if err := daemon.ValidateRelate(req); err != nil {
			return err
		}
		entry, err := reg.SoleEntry()
		if err != nil {
			return err
		}
		name := "relate-" + req.PMKey
		if docker, err := devcontainer.NewDockerClient(); err == nil {
			_ = (&agent.Manager{Docker: docker}).Delete(context.Background(), name)
			_ = docker.Close()
		}
		return dockerAgentLauncher{daemonID: daemonID}.Launch(context.Background(), name, "/human-relate "+req.PMKey, entry.Dir, entry.Dir)
	}
}

// featuresGeneratorFunc builds the daemon's FeaturesGenerator closure: it
// launches the human-features skill in the registered project's devcontainer,
// exactly like a board stage transition, so the desktop Features pane's
// Generate/Refresh button reuses the same containerized agent path.
func featuresGeneratorFunc(reg *daemon.ProjectRegistry) func() error {
	return func() error {
		entry, err := reg.SoleEntry()
		if err != nil {
			return err
		}
		// Tear down any prior "features" agent first so Generate/Refresh is
		// idempotent — Manager.Start refuses to start over a still-running agent,
		// so without this a second click fails with "agent already running".
		if docker, err := devcontainer.NewDockerClient(); err == nil {
			_ = (&agent.Manager{Docker: docker}).Delete(context.Background(), "features")
			_ = docker.Close()
		}
		return dockerAgentLauncher{}.Launch(context.Background(), "features", "/human-features", entry.Dir, entry.Dir)
	}
}

// findbugsRunnerFunc builds the daemon's FindbugsRunner closure: it launches the
// human-findbugs sweep in the registered project's devcontainer, the same
// containerized agent path as feature generation. The sweep resolves the PM
// tracker itself (via `human tracker topology`, forwarded to this daemon) and
// files each surviving finding as a bug ticket, so the button needs no argument.
func findbugsRunnerFunc(reg *daemon.ProjectRegistry) func() error {
	return func() error {
		entry, err := reg.SoleEntry()
		if err != nil {
			return err
		}
		// Tear down any prior "findbugs" agent first so a re-click after a stale
		// or crashed run is idempotent — Manager.Start refuses to start over a
		// still-running agent.
		if docker, err := devcontainer.NewDockerClient(); err == nil {
			_ = (&agent.Manager{Docker: docker}).Delete(context.Background(), "findbugs")
			_ = docker.Close()
		}
		return dockerAgentLauncher{}.Launch(context.Background(), "findbugs", "/human-findbugs", entry.Dir, entry.Dir)
	}
}

// securityRunnerFunc builds the daemon's SecurityRunner closure: it launches the
// human-security sweep in the registered project's devcontainer — the Security
// pane's counterpart to findbugsRunnerFunc. The scan resolves the PM tracker
// itself and files each surviving vulnerability as a security ticket, so the
// button needs no argument.
func securityRunnerFunc(reg *daemon.ProjectRegistry) func() error {
	return func() error {
		entry, err := reg.SoleEntry()
		if err != nil {
			return err
		}
		// Tear down any prior "findsecurity" agent first so a re-click after a
		// stale or crashed run is idempotent — Manager.Start refuses to start over
		// a still-running agent.
		if docker, err := devcontainer.NewDockerClient(); err == nil {
			_ = (&agent.Manager{Docker: docker}).Delete(context.Background(), "findsecurity")
			_ = docker.Close()
		}
		return dockerAgentLauncher{}.Launch(context.Background(), "findsecurity", "/human-security", entry.Dir, entry.Dir)
	}
}

// mockupsCreatorFunc builds the daemon's MockupsCreator closure: it records
// the ticket→mockup-set link in the project's .human/mockups.json and launches
// the human-mockups skill in the registered project's devcontainer — the same
// containerized agent path as feature generation. The link is written BEFORE
// the launch (it doubles as the board's "creating…" marker) and rolled back if
// the launch fails, so the menu never sticks on a set that was never started.
func mockupsCreatorFunc(reg *daemon.ProjectRegistry) func(daemon.CreateMocksRequest) error {
	return func(req daemon.CreateMocksRequest) error {
		entry, err := reg.EntryForKey(req.PMKey)
		if err != nil {
			return err
		}
		slug := mockups.SlugFor(req.PMKey)
		if slug == "" {
			return errors.WithDetails("cannot derive mockup slug", "pm_key", req.PMKey)
		}
		// Tear down any prior agent for this ticket first so a retry after a
		// stale or crashed run is idempotent — Manager.Start refuses to start
		// over a still-running agent.
		agentName := "mockups-" + slug
		if docker, err := devcontainer.NewDockerClient(); err == nil {
			_ = (&agent.Manager{Docker: docker}).Delete(context.Background(), agentName)
			_ = docker.Close()
		}
		store := mockups.NewStore(mockups.PathIn(entry.Dir))
		if err := store.Set(req.PMKey, mockups.Entry{Slug: slug, Created: time.Now()}); err != nil {
			return err
		}
		prompt := "/human-mockups " + req.PMKey + ": " + req.PMTitle
		if req.Description != "" {
			prompt += "\n\nTicket context:\n" + req.Description
		}
		if err := (dockerAgentLauncher{}).Launch(context.Background(), agentName, prompt, entry.Dir, entry.Dir); err != nil {
			_ = store.Delete(req.PMKey)
			return err
		}
		return nil
	}
}

// childManifest is the minimal index.json placeholder the daemon writes to
// reserve a variation group's directory before launching the agent. internal/
// daemon cannot import desktop's MockupSet, so this mirrors its JSON tags. Zero
// options keeps validMockupSet false, so the reserved group stays hidden from
// the viewer until the agent fills it in.
type childManifest struct {
	Slug         string   `json:"slug"`
	Feature      string   `json:"feature"`
	Ticket       string   `json:"ticket,omitempty"`
	Parent       string   `json:"parent,omitempty"`
	ParentFile   string   `json:"parentFile,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
	Created      string   `json:"created"`
	Options      []string `json:"options"`
}

// leadingDigits returns the run of digits at the start of s ("03-foo.html" →
// "03"), or "" when s does not begin with a digit.
func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// nextVariationSlug derives a human-readable, collision-free slug for a new
// variation group: <parentSlug>-o<optN>-v<K>, where optN is parentFile's
// leading option number (omitted when parentFile has none) and K is the
// smallest positive integer with no existing mockups/<slug>/ directory. The
// daemon reserves the dir before launch, so scanning for the free K is race-safe
// against sequential creations; concurrent creations pick distinct K because
// each reserves its dir before the next scan.
func nextVariationSlug(projectDir, parentSlug, parentFile string) string {
	prefix := parentSlug
	if digits := leadingDigits(parentFile); digits != "" {
		// Strip leading zeros for readability ("03" → "3"); "0" stays "0".
		trimmed := strings.TrimLeft(digits, "0")
		if trimmed == "" {
			trimmed = "0"
		}
		prefix += "-o" + trimmed
	}
	mockupsDir := filepath.Join(projectDir, "mockups")
	for k := 1; ; k++ {
		slug := fmt.Sprintf("%s-v%d", prefix, k)
		if _, err := os.Stat(filepath.Join(mockupsDir, slug)); os.IsNotExist(err) {
			return slug
		}
	}
}

// variationsCreatorFunc builds the daemon's VariationsCreator closure: it
// reserves a child group directory (a 0-option manifest placeholder that stays
// hidden from the viewer) and launches human-mockups in variation mode. The
// directory is the "creating" marker (NOT the store, which is keyed one entry
// per ticket for the root link + winner); a launch failure rolls the directory
// back so navigation never shows a group that was never started.
func variationsCreatorFunc(reg *daemon.ProjectRegistry) func(daemon.CreateVariationsRequest) error {
	return func(req daemon.CreateVariationsRequest) error {
		entry, err := reg.EntryForKey(req.PMKey)
		if err != nil {
			return err
		}
		if req.ParentSlug == "" || req.ParentFile == "" {
			return errors.WithDetails("variation requires a parent slug and file",
				"parent_slug", req.ParentSlug, "parent_file", req.ParentFile)
		}
		childSlug := nextVariationSlug(entry.Dir, req.ParentSlug, req.ParentFile)

		// Idempotent retry: tear down any prior agent for this child slug so a
		// re-launch after a crash is not blocked by a still-running agent.
		agentName := "mockups-" + childSlug
		if docker, err := devcontainer.NewDockerClient(); err == nil {
			_ = (&agent.Manager{Docker: docker}).Delete(context.Background(), agentName)
			_ = docker.Close()
		}

		childDir := filepath.Join(entry.Dir, "mockups", childSlug)
		if err := os.MkdirAll(childDir, 0o700); err != nil {
			return errors.WrapWithDetails(err, "reserve variation group dir", "dir", childDir)
		}
		manifest := childManifest{
			Slug:         childSlug,
			Feature:      req.Feature,
			Ticket:       req.PMKey,
			Parent:       req.ParentSlug,
			ParentFile:   req.ParentFile,
			Instructions: req.Instructions,
			Created:      time.Now().UTC().Format(time.RFC3339),
			Options:      []string{},
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			_ = os.RemoveAll(childDir)
			return errors.WrapWithDetails(err, "marshal variation placeholder", "slug", childSlug)
		}
		if err := os.WriteFile(filepath.Join(childDir, "index.json"), data, 0o600); err != nil {
			_ = os.RemoveAll(childDir)
			return errors.WrapWithDetails(err, "write variation placeholder", "slug", childSlug)
		}

		prompt := "/human-mockups --variation " + req.PMKey + ": " + req.Feature +
			"\n\nVary this existing mockup:" +
			"\n  group slug: " + req.ParentSlug +
			"\n  source file: mockups/" + req.ParentSlug + "/" + req.ParentFile +
			"\nWrite the new group to: mockups/" + childSlug + "/" +
			"\nChange instructions:\n" + req.Instructions
		if err := (dockerAgentLauncher{}).Launch(context.Background(), agentName, prompt, entry.Dir, entry.Dir); err != nil {
			_ = os.RemoveAll(childDir)
			return err
		}
		return nil
	}
}

// mockupChooserFunc builds the daemon's MockupChooser closure: record the
// ticket's winner (validating the target file exists) or clear it when Slug is
// empty.
func mockupChooserFunc(reg *daemon.ProjectRegistry) func(daemon.ChooseMockupRequest) error {
	return func(req daemon.ChooseMockupRequest) error {
		entry, err := reg.EntryForKey(req.PMKey)
		if err != nil {
			return err
		}
		store := mockups.NewStore(mockups.PathIn(entry.Dir))
		if req.Slug == "" {
			return store.ClearChoice(req.PMKey)
		}
		target := filepath.Join(entry.Dir, "mockups", req.Slug, req.File)
		if _, err := os.Stat(target); err != nil {
			return errors.WrapWithDetails(err, "chosen mockup not found",
				"slug", req.Slug, "file", req.File)
		}
		return store.Choose(req.PMKey, mockups.Choice{Slug: req.Slug, File: req.File})
	}
}

// mockupPrunerFunc builds the daemon's MockupPruner closure: archive a
// variation subtree (the group plus every transitive descendant) under
// mockups/.archive/, refusing to prune a ticket's root group. If the ticket's
// current winner lives inside the pruned subtree, the winner is cleared so no
// dangling choice survives.
func mockupPrunerFunc(reg *daemon.ProjectRegistry) func(daemon.PruneMockupRequest) error {
	return func(req daemon.PruneMockupRequest) error {
		entry, err := reg.EntryForKey(req.PMKey)
		if err != nil {
			return err
		}
		if req.Slug == "" {
			return errors.WithDetails("prune requires a group slug")
		}
		if req.Slug == mockups.SlugFor(req.PMKey) {
			return errors.WithDetails("cannot prune the root mockup group", "slug", req.Slug)
		}
		mockupsDir := filepath.Join(entry.Dir, "mockups")
		subtree := variationSubtree(mockupsDir, req.Slug)

		archiveRoot := filepath.Join(mockupsDir, ".archive")
		if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
			return errors.WrapWithDetails(err, "create archive dir", "dir", archiveRoot)
		}
		for _, slug := range subtree {
			src := filepath.Join(mockupsDir, slug)
			if _, err := os.Stat(src); err != nil {
				continue // already gone; nothing to archive
			}
			if err := os.Rename(src, filepath.Join(archiveRoot, slug)); err != nil {
				return errors.WrapWithDetails(err, "archive pruned group", "slug", slug)
			}
		}

		store := mockups.NewStore(mockups.PathIn(entry.Dir))
		if chosen, ok := store.ChosenFor(req.PMKey); ok {
			for _, slug := range subtree {
				if chosen.Slug == slug {
					return store.ClearChoice(req.PMKey)
				}
			}
		}
		return nil
	}
}

// variationSubtree returns the slug plus every transitive descendant, computed
// from the parent links each group's index.json records. An orphan (parent dir
// gone) simply contributes no children, so navigation and pruning stay
// consistent even after a manual deletion.
func variationSubtree(mockupsDir, root string) []string {
	children := map[string][]string{}
	dirEntries, err := os.ReadDir(mockupsDir)
	if err == nil {
		for _, e := range dirEntries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(mockupsDir, e.Name(), "index.json")) // #nosec G304 — registered project dir
			if rerr != nil {
				continue
			}
			var m childManifest
			if json.Unmarshal(data, &m) != nil || m.Parent == "" {
				continue
			}
			children[m.Parent] = append(children[m.Parent], e.Name())
		}
	}
	var out []string
	var walk func(slug string)
	walk = func(slug string) {
		out = append(out, slug)
		for _, c := range children[slug] {
			walk(c)
		}
	}
	walk(root)
	return out
}

// hostClaudeIdeationRunner implements daemon.IdeationRunner by running one
// headless `claude -p` turn on the daemon host in the registered project dir.
// Session continuity across turns rides on claude's own --resume store, so the
// daemon holds no conversation state beyond the resume id.
type hostClaudeIdeationRunner struct {
	reg *daemon.ProjectRegistry
}

// claudeTurnOutput is the subset of `claude -p --output-format json` we need.
type claudeTurnOutput struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
}

func (r hostClaudeIdeationRunner) Run(ctx context.Context, resumeID, prompt string) (daemon.IdeationTurn, error) {
	entry, err := r.reg.SoleEntry()
	if err != nil {
		return daemon.IdeationTurn{}, err
	}
	// Read-only tool allowlist: the agent may inspect the repo but nothing
	// else; the daemon, not the agent, writes the ticket. Single argv element
	// so the variadic flag cannot swallow the positional prompt.
	args := []string{"-p", prompt, "--output-format", "json", "--allowedTools", "Read Grep Glob"}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	cmd := exec.CommandContext(ctx, "claude", args...) // #nosec G204 -- fixed binary, prompt is a discrete argv element
	cmd.Dir = entry.Dir
	out, err := cmd.Output()
	// Live-verified (CLI 2.1.193): on turn failure claude exits non-zero,
	// writes the result JSON with is_error:true and the cause in `result` to
	// STDOUT, and leaves stderr empty. So the JSON parse below must run on
	// both the success and the ExitError path; stderr is only meaningful for
	// true exec failures (binary missing, process killed).
	var parsed claudeTurnOutput
	parseErr := json.Unmarshal(out, &parsed)
	if parseErr == nil && parsed.IsError {
		return daemon.IdeationTurn{}, errors.WithDetails("ideation agent turn failed", "result", parsed.Result)
	}
	if err != nil {
		if ctx.Err() != nil {
			return daemon.IdeationTurn{}, errors.WrapWithDetails(ctx.Err(), "ideation agent turn timed out")
		}
		detail := ""
		if ee, ok := goerrors.AsType[*exec.ExitError](err); ok {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		return daemon.IdeationTurn{}, errors.WrapWithDetails(err, "running ideation agent turn", "stderr", detail)
	}
	if parseErr != nil {
		return daemon.IdeationTurn{}, errors.WrapWithDetails(parseErr, "parsing ideation agent output")
	}
	return daemon.IdeationTurn{Reply: parsed.Result, ResumeID: parsed.SessionID}, nil
}

// resolvePMCreator resolves the PM-role tracker.Creator and its first
// configured project. Role-based, never key-prefix — mirrors resolvePMCommenter.
func resolvePMCreator(dir string, lookup config.EnvLookup, resolver *vault.Resolver) (tracker.Creator, string, error) {
	instances, err := cmdutil.LoadAllInstancesWithResolver(dir, lookup, resolver)
	if err != nil {
		return nil, "", err
	}
	for _, inst := range instances {
		if inst.InferRole() != "pm" {
			continue
		}
		// tracker.Provider embeds Creator, so this assertion cannot fail
		// today; kept for symmetry with resolvePMCommenter and as a guard
		// should the Provider interface ever be split.
		c, ok := inst.Provider.(tracker.Creator)
		if !ok {
			continue
		}
		target := inst.FilingTarget()
		if target == "" {
			// A PM tracker with neither projects nor create_in would file
			// group-less and land the ticket off every board (SC-1959). Fail
			// loudly instead of creating an invisible ticket.
			return nil, "", errors.WithDetails(
				"PM tracker has no filing target — set create_in (or projects) in .humanconfig so new tickets land on the board",
				"tracker", inst.Name, "dir", dir)
		}
		return c, target, nil
	}
	return nil, "", errors.WithDetails("no PM-role tracker configured", "dir", dir)
}

// resolvePMEditor resolves the PM-role tracker.Editor for evolve-mode idea
// promotion. Role-based, never key-prefix — mirrors resolvePMCommenter.
func resolvePMEditor(dir string, lookup config.EnvLookup, resolver *vault.Resolver) (tracker.Editor, error) {
	instances, err := cmdutil.LoadAllInstancesWithResolver(dir, lookup, resolver)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.InferRole() != "pm" {
			continue
		}
		if ed, ok := inst.Provider.(tracker.Editor); ok {
			return ed, nil
		}
	}
	return nil, errors.WithDetails("no PM-role tracker with edit support configured", "dir", dir)
}

// ideationEngine wires the board ideation engine: host claude runner, role-
// resolved PM creator/editor, and a hook-store poke so the created card
// reaches the board through the existing subscribe/refetch loop.
func ideationEngine(reg *daemon.ProjectRegistry, resolver *vault.Resolver, hookStore *daemon.HookEventStore, store daemon.IdeationStore, logger zerolog.Logger) *daemon.IdeationEngine {
	firstEntry := func() (daemon.ProjectEntry, error) {
		return reg.SoleEntry()
	}
	return &daemon.IdeationEngine{
		Runner: hostClaudeIdeationRunner{reg: reg},
		ResolveCreator: func() (tracker.Creator, string, error) {
			entry, err := firstEntry()
			if err != nil {
				return nil, "", err
			}
			return resolvePMCreator(entry.Dir, entry.EnvLookup(), resolver)
		},
		ResolveEditor: func() (tracker.Editor, error) {
			entry, err := firstEntry()
			if err != nil {
				return nil, err
			}
			return resolvePMEditor(entry.Dir, entry.EnvLookup(), resolver)
		},
		Notify: func() {
			hookStore.Append(hookevents.Event{EventName: "IdeationCreated", Timestamp: time.Now().UTC()})
		},
		Store:  store,
		Logger: logger,
	}
}

// restoreIdeationSession brings back a chat interrupted by a restart — the
// self-restart handover lands between turns, exactly when the user is composing
// a reply. A finished or stale session is left behind rather than resurrected.
func restoreIdeationSession(engine *daemon.IdeationEngine, store daemon.IdeationStore, logger zerolog.Logger) {
	if engine == nil || store == nil {
		return
	}
	saved, err := store.Load()
	if err != nil {
		logger.Warn().Err(err).Msg("loading persisted ideation session failed")
		return
	}
	if saved == nil {
		return
	}
	if engine.Restore(*saved, time.Now(), daemon.IdeationMaxAge) {
		logger.Info().Str("session", saved.ID).Str("state", string(saved.State)).Msg("restored ideation session")
	}
}

// boardPMCommenterFunc resolves the PM commenter for the board failure watcher,
// signed so every marker it posts carries the daemon id as its machine: field
// and BuildRevision as its build: field — the same choke-point signing the
// board-transition engine gets, so the failure/outage/deploy posters inherit
// provenance without a per-writer stamp.
func boardPMCommenterFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string) func() (tracker.Commenter, error) {
	return func() (tracker.Commenter, error) {
		entry, err := reg.SoleEntry()
		if err != nil {
			return nil, err
		}
		commenter, err := resolvePMCommenter(entry.Dir, entry.EnvLookup(), resolver)
		if err != nil {
			return nil, err
		}
		return marker.NewSigningCommenter(commenter, daemonID, daemon.BuildRevision()), nil
	}
}

// boardHasWatchers reports whether any UI (board or TUI) is subscribed, so the
// freshness poll can skip tracker work when no board is open. A nil store (hook
// tracking disabled) reads as no watchers.
func boardHasWatchers(events *daemon.HookEventStore) func() bool {
	return func() bool { return events != nil && events.SubscriberCount() > 0 }
}

// boardReconcileListerFunc enumerates open PM cards with their comment threads
// for the durable reconcile pass. It reuses the listTrackerIssues fan-out, then
// fetches each open PM ticket's comments (skipping ideas, which carry no
// pipeline markers) — mirroring scanReadyForReview's fan-out without altering
// it. Best-effort: a per-ticket error drops that ticket, not the whole tick, so
// one flaky tracker call never blocks recovery of the rest.
func boardReconcileListerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, logger zerolog.Logger) daemon.ReconcileLister {
	return func(ctx context.Context) ([]daemon.ReconcileCard, error) {
		jobs, results, err := listTrackerIssues(reg, resolver)
		if err != nil {
			return nil, err
		}
		var cards []daemon.ReconcileCard
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := range results {
			if results[i].TrackerRole != "pm" || results[i].Err != "" {
				continue
			}
			commenter, ok := jobs[i].inst.Provider.(tracker.Commenter)
			if !ok {
				continue
			}
			for _, issue := range results[i].Issues {
				// Idea tickets carry no pipeline markers, so they can never be an
				// orphaned handoff — skip the per-issue comment round-trip.
				if issue.IsIdea() {
					continue
				}
				wg.Add(1)
				go func(c tracker.Commenter, key string) {
					defer wg.Done()
					fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()
					comments, err := c.ListComments(fetchCtx, key)
					if err != nil {
						// Best-effort per SC intent: dropping this ticket from the tick is fine,
						// but do it visibly — a silent swallow hid a flaky tracker shrinking the
						// reconcile set (1700).
						logger.Warn().Str("key", key).Err(err).
							Msg("reconcile comment fetch failed; skipping ticket this tick")
						return
					}
					mu.Lock()
					cards = append(cards, daemon.ReconcileCard{Key: key, Comments: comments})
					mu.Unlock()
				}(commenter, issue.Key)
			}
		}
		wg.Wait()
		return cards, nil
	}
}

// dockerAgentSweeper implements daemon.AgentZombieSweeper using real Docker and agent metadata.
type dockerAgentSweeper struct{}

func (s *dockerAgentSweeper) RunningAgents() ([]daemon.AgentInfo, error) {
	metas, err := agent.ListMetas()
	if err != nil {
		return nil, err
	}
	var result []daemon.AgentInfo
	for _, m := range metas {
		if m.Status != agent.StatusRunning {
			continue
		}
		result = append(result, daemon.AgentInfo{
			Name:        m.Name,
			ContainerID: m.ContainerID,
			CreatedAt:   m.CreatedAt,
			// A bare `human agent start NAME` persists an empty Prompt and never
			// launches claude (agent.Manager.Start only execs claude when a
			// prompt is present), so an empty Prompt marks an idle-by-design
			// agent the sweep must not mistake for a crashed one (SC-236).
			Idle: m.Prompt == "",
		})
	}
	return result, nil
}

func (s *dockerAgentSweeper) IsProcessRunning(ctx context.Context, containerID string, process string) (bool, error) {
	docker, err := devcontainer.NewDockerClient()
	if err != nil {
		return false, err
	}
	defer func() { _ = docker.Close() }()

	execID, err := docker.ExecCreate(ctx, containerID, []string{"pgrep", "-x", process}, devcontainer.ExecOptions{})
	if err != nil {
		return false, err
	}
	resp, err := docker.ExecAttach(ctx, execID)
	if err != nil {
		return false, err
	}
	// Drain the multiplexed stream to EOF before inspecting: ExecInspect's exit
	// code is only reliable once the exec has finished and the stream closed.
	// A stalled stream must not park this call (it runs inline on the single
	// zombie-sweep goroutine): the watchdog closes resp on ctx.Done, unblocking
	// the drain (SC-427).
	stop := closeExecOnContextDone(ctx, resp)
	_, _ = io.Copy(io.Discard, resp.Reader)
	stop()
	_ = resp.Close()

	inspect, err := docker.ExecInspect(ctx, execID)
	if err != nil {
		return false, err
	}
	return inspect.ExitCode == 0, nil
}

// closeExecOnContextDone starts a watchdog that closes the exec attachment when
// ctx is cancelled, unblocking a drain parked on a stalled stream (closing
// resp closes its underlying conn). It returns a stop func the caller invokes
// once the drain has finished, tearing the watchdog down.
func closeExecOnContextDone(ctx context.Context, resp devcontainer.ExecAttachResponse) (stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = resp.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

// agentClaudeAlive reports whether the named agent's claude process is still
// running, reusing the zombie sweep's own liveness check so both teardown paths
// ask the container the same question. A missing meta or an empty container id
// means there is nothing left to be alive; any other failure is returned so the
// caller can decide (the cleanup listener treats unreachable as ended, SC-3785).
func agentClaudeAlive(ctx context.Context, name string) (bool, error) {
	meta, err := agent.ReadMeta(name)
	if err != nil {
		return false, err
	}
	if meta.ContainerID == "" {
		return false, nil
	}
	return (&dockerAgentSweeper{}).IsProcessRunning(ctx, meta.ContainerID, "claude")
}

func (s *dockerAgentSweeper) DeleteAgent(ctx context.Context, name string) error {
	docker, err := devcontainer.NewDockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = docker.Close() }()

	// The zombie sweep reaps a run that is gone/unresponsive: mark it StatusFailed
	// before teardown so stopReason records outcome.json Reason:"reaped" (correct
	// diagnosis) — never a spurious "completed". No handoff was posted, so the
	// worktree is preserved for forensics regardless (SC-731). Best-effort: a
	// missing meta just means it was already torn down.
	if meta, readErr := agent.ReadMeta(name); readErr == nil && meta.Status != agent.StatusFailed {
		meta.Status = agent.StatusFailed
		_ = agent.WriteMeta(meta)
	}

	mgr := &agent.Manager{Docker: docker}
	return mgr.Delete(ctx, name)
}

// NewForgeDeployer returns the production Deployer — push + PR, CI gate,
// freshness rebase, merge — shared by the board's Deploy stage and the
// human deploy CLI command, so there is exactly one deploy implementation.
func NewForgeDeployer(resolver *vault.Resolver, lookup config.EnvLookup) daemon.Deployer {
	return forgeDeployer{resolver: resolver, lookup: lookup}
}

// startSleepInhibitor holds a systemd suspend block while agents run so an
// auto-suspending desktop cannot freeze Docker and the pipeline mid-run
// (SC-262). Off by default; the toggle is re-read each tick so a settings-UI
// change applies without a restart.
func startSleepInhibitor(ctx context.Context, out io.Writer, logger zerolog.Logger) {
	go daemon.RunSleepInhibitor(ctx, &dockerAgentSweeper{}, logindInhibitor{},
		func() bool {
			cfg, err := daemon.LoadPowerConfig(".")
			if err != nil {
				logger.Warn().Err(err).Msg("sleep inhibitor: cannot read power config; treating as disabled")
				return false
			}
			return cfg.InhibitSleep
		},
		daemon.SleepInhibitInterval, logger)
	if inhibitCfg, _ := daemon.LoadPowerConfig("."); inhibitCfg.InhibitSleep {
		_, _ = fmt.Fprintln(out, "Sleep inhibition: enabled (suspend deferred while agents run)")
	} else {
		_, _ = fmt.Fprintln(out, "Sleep inhibition: disabled")
	}
}

// attachActivity fills each running card's Activity from the phase records the
// run itself wrote (stage.triage, stage.verify, …).
//
// Nothing here is new information. Every stage already writes its phase with a
// timestamp so the next stage — or a fresh agent taking over from one that died
// — can read back what it learned; the board has simply never looked. Until it
// does, an entire fix run renders as one unchanging "fixing…" that reads
// identically at thirty seconds, at fourteen hours, and when the agent behind it
// has been dead since the previous afternoon.
//
// Only running cards are read: a finished card's last phase is history, and
// showing it would suggest work still in flight. A store that will not open, or
// a scope with nothing recorded, leaves the card exactly as it was — the badge
// degrades to today's behaviour rather than inventing a phase.
func attachActivity(ctx context.Context, reg *daemon.ProjectRegistry, view *daemon.BoardView, logger zerolog.Logger) {
	if view == nil || len(view.Cards) == 0 {
		return
	}
	err := withStateStore(func(store agentstate.Store) error {
		for i, card := range view.Cards {
			if card.State != string(daemon.BoardRunning) {
				continue
			}
			entries, err := store.List(ctx, boardStateProject(reg, card.Key), card.Key, board.StagePrefix)
			if err != nil || len(entries) == 0 {
				continue
			}
			phase, at := board.LatestActivity(entries)
			if phase == "" {
				continue
			}
			view.Cards[i].Activity = board.ActivityLabel(phase)
			view.Cards[i].ActivityAt = at.UTC().Format(time.RFC3339)
		}
		return nil
	})
	if err != nil {
		// The phase is an enrichment, never a gate: a board that cannot read the
		// state store still renders every card it fetched.
		logger.Debug().Err(err).Msg("board view: could not read phase records; cards render without them")
	}
}

// whereCommentsFunc loads one ticket's thread for the fsm-where route: the
// comments the placement is derived from, plus the status and idea label that
// take an item off the board entirely.
//
// Modelled on issueGetterFunc rather than sharing it, because that path builds
// the desktop panel's extras and caches them; a question about where an item is
// must read the thread as it stands now.
func whereCommentsFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) daemon.WhereCommentReader {
	return func(key string) ([]tracker.Comment, tracker.Category, bool, error) {
		entry, err := reg.EntryForKey(key)
		if err != nil {
			return nil, "", false, err
		}
		instances, err := cmdutil.LoadAllInstancesWithResolver(entry.Dir, entry.EnvLookup(), resolver)
		if err != nil {
			return nil, "", false, err
		}
		inst, err := tracker.Resolve("", instances, key)
		if err != nil {
			return nil, "", false, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		issue, err := inst.Provider.GetIssue(ctx, key)
		if err != nil {
			return nil, "", false, err
		}
		lister, ok := inst.Provider.(tracker.Commenter)
		if !ok {
			return nil, issue.StatusType, issue.IsIdea(), nil
		}
		comments, err := lister.ListComments(ctx, key)
		if err != nil {
			// The thread is what the placement is derived from, so an unreadable
			// one must fail rather than answer "backlog, nothing here" — that
			// would be a confident wrong answer where none is much cheaper.
			return nil, "", false, err
		}
		return comments, issue.StatusType, issue.IsIdea(), nil
	}
}
