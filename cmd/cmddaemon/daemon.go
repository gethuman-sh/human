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
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/audit"
	"github.com/gethuman-sh/human/internal/chrome"
	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/codenav"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/devcontainer"
	"github.com/gethuman-sh/human/internal/dispatch"
	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/gitrepo"
	"github.com/gethuman-sh/human/internal/messaging/slack"
	"github.com/gethuman-sh/human/internal/messaging/telegram"
	"github.com/gethuman-sh/human/internal/mockups"
	"github.com/gethuman-sh/human/internal/proxy"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

const daemonChildEnv = "_HUMAN_DAEMON_CHILD"

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
	vaultResolver *vault.Resolver
	statsStore    *stats.StatsStore
	statsWriter   *stats.Writer
	auditStore    *audit.Store
	auditWriter   *audit.Writer
	confirmDB     *daemon.ConfirmDB
	daemonID      string
}

// runMaintenanceLoop periodically cleans up stale pending confirmations and
// prunes the stats, audit, and agent-execution-log stores past their retention
// windows. It runs until ctx is cancelled.
func runMaintenanceLoop(ctx context.Context, logger zerolog.Logger, confirmStore *daemon.PendingConfirmStore, statsStore *stats.StatsStore, auditStore *audit.Store) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			confirmStore.Cleanup(daemon.ConfirmRetention)
			if statsStore != nil {
				if _, pruneErr := statsStore.Prune(ctx); pruneErr != nil {
					logger.Warn().Err(pruneErr).Msg("periodic stats prune failed")
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
		}
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

	if err := WritePidFile(os.Getpid()); err != nil {
		return nil, errors.WrapWithDetails(err, "failed to write PID file")
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
		DaemonID:   daemonID,
		Projects:   projectInfos,
	}
	if err := daemon.WriteInfo(info); err != nil {
		return nil, errors.WrapWithDetails(err, "failed to write daemon info")
	}

	printStartBanner(out, token, daemonID, addr, chromeAddr, proxyAddr, daemonAddr, chromeFullAddr, proxyFullAddr, projectInfos)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := newDaemonLogger(debug)
	vaultResolver := buildVaultResolver(projectRegistry, logger)

	// Turn a silent split->single topology fallback into a loud startup signal:
	// a tracker declared role: engineering whose token does not resolve would run
	// single-tracker here and split elsewhere from the same config (SC-660 rule 7).
	warnTopologyDivergence(projectRegistry, vaultResolver, out, logger)

	connTracker := daemon.NewConnectedTracker()
	// Persist hook events to the host so they survive the in-memory ring's
	// eviction and daemon restarts, keyed to the emitting agent's execution log.
	hookStore := daemon.NewHookEventStore().WithPersistence(agent.HookEventSink)
	networkStore := daemon.NewNetworkEventStore()
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

	go runMaintenanceLoop(ctx, logger, confirmStore, statsStore, auditStore)

	// Keep the shared code-navigation index fresh so every agent, worktree, and
	// the developer's CLI query one daemon-owned index instead of each rebuilding
	// it (SC-781).
	go runCodenavIndexLoop(ctx, projectRegistry, codenav.DefaultDBPath(), logger)

	doctor := daemon.NewDoctorRunner(buildDoctorChecks(projectRegistry, vaultResolver, doctorPersistence{
		stats:    statsStore != nil,
		audit:    auditStore != nil,
		confirms: confirmDB != nil,
	}))

	srv := &daemon.Server{
		Addr:              addr,
		Token:             token,
		SafeMode:          safe,
		DaemonStartedAt:   time.Now().UTC(),
		CmdFactory:        cmdFactory,
		Logger:            logger,
		ConnectedPIDs:     connTracker,
		HookEvents:        hookStore,
		NetworkEvents:     networkStore,
		IssueFetcher:      fetchTrackerIssuesFunc(projectRegistry, vaultResolver),
		LiteIssueFetcher:  fetchTrackerIssuesLiteFunc(projectRegistry, vaultResolver),
		IssueGetter:       daemon.NewCachedIssueGetter(issueGetterFunc(projectRegistry, vaultResolver)),
		TrackerDiagnoser:  trackerDiagnoserFunc(projectRegistry, vaultResolver),
		Doctor:            doctor,
		Projects:          projectRegistry,
		PendingConfirms:   confirmStore,
		StatsWriter:       statsWriter,
		StatsStore:        statsStore,
		AuditSink:         auditWriter,
		AuditStore:        auditStore,
		AgentCleaner:      &dockerAgentCleaner{},
		VaultResolver:     vaultResolver,
		BoardTransitioner: boardTransitionerFunc(projectRegistry, vaultResolver, daemonID, logger),
		BoardFixer:        boardFixerFunc(projectRegistry, vaultResolver, daemonID, logger),
		BoardOptioner:     boardOptionerFunc(projectRegistry, vaultResolver, daemonID, logger),
		BugCreator:        bugCreatorFunc(projectRegistry, vaultResolver),
		CloseTicketer:     closeTicketerFunc(projectRegistry, vaultResolver),
		FeaturesGenerator: featuresGeneratorFunc(projectRegistry),
		MockupsCreator:    mockupsCreatorFunc(projectRegistry),
		Ideation:          ideationEngine(projectRegistry, vaultResolver, hookStore, logger),
	}

	return &daemonState{
		srv:           srv,
		ctx:           ctx,
		stop:          stop,
		logger:        logger,
		connTracker:   connTracker,
		networkStore:  networkStore,
		vaultResolver: vaultResolver,
		statsStore:    statsStore,
		statsWriter:   statsWriter,
		auditStore:    auditStore,
		auditWriter:   auditWriter,
		confirmDB:     confirmDB,
		daemonID:      daemonID,
	}, nil
}

// runDaemonForeground runs the daemon in the current process (blocking).
// It writes a PID file on start and removes it on shutdown.
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

	ds, err := initDaemon(cmd, addr, chromeAddr, proxyAddr, safe, debug, projectDirs, cmdFactory, version)
	if err != nil {
		return err
	}
	defer RemovePidFile()
	defer daemon.RemoveInfo()
	defer ds.stop()
	if ds.statsWriter != nil {
		defer ds.statsWriter.Close()
	}
	if ds.statsStore != nil {
		defer func() { _ = ds.statsStore.Close() }()
	}
	if ds.auditWriter != nil {
		defer ds.auditWriter.Close()
	}
	if ds.auditStore != nil {
		defer func() { _ = ds.auditStore.Close() }()
	}
	if ds.confirmDB != nil {
		defer func() { _ = ds.confirmDB.Close() }()
	}

	out := cmd.OutOrStdout()
	ctx := ds.ctx
	logger := ds.logger

	startChromeServices(ctx, chromeAddr, ds.srv.Token, logger)

	proxySrv, proxyStatus, proxyErr := buildProxyServer(proxyAddr, interactive, logger, ds.networkStore)
	if proxyErr != nil {
		return proxyErr
	}
	if proxyStatus != "" {
		_, _ = fmt.Fprintln(out, proxyStatus)
	}

	go func() {
		if err := proxySrv.ListenAndServe(ctx); err != nil {
			logger.Error().Err(err).Msg("https proxy failed")
		}
	}()

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
		proxy.RemoveStats(statsPath)
		daemon.RemoveConnected(connectedPath)
	}()

	cwd, _ := os.Getwd()
	if unmount := fuseMount(cwd, safe, logger); unmount != nil {
		defer unmount()
	}

	slackNotifier, slackStatus := startSlackNotifier(logger, ds.vaultResolver)
	if slackStatus != "" {
		_, _ = fmt.Fprintln(out, "Slack notifications:", slackStatus)
	}

	telegramStatus := startTelegramDispatcher(ctx, logger, slackNotifier, ds.vaultResolver)
	_, _ = fmt.Fprintln(out, "Telegram dispatch:", telegramStatus)

	if err := claude.InstallHooks(out, claude.OSFileWriter{}); err != nil {
		logger.Warn().Err(err).Msg("hook upgrade failed")
	}

	go daemon.RunAgentCleanup(ctx, ds.srv.HookEvents, &dockerAgentCleaner{}, logger)
	hookEvents := ds.srv.HookEvents
	go daemon.RunAgentZombieSweep(ctx, &dockerAgentSweeper{}, func(agentName string) {
		// A reaped agent died without firing hooks, so no exit event exists
		// for the board failure watcher to act on; synthesizing one converges
		// the reap path with the hook-driven exit paths — one marker-posting
		// code path (SC-206).
		hookEvents.Append(hookevents.Event{
			EventName: "StopFailure",
			AgentName: agentName,
			Timestamp: time.Now().UTC(),
		})
	}, logger)
	boardTransition := boardTransitionerFunc(ds.srv.Projects, ds.vaultResolver, ds.daemonID, logger)
	// A finished build chains straight into its review — the board's
	// auto-review; the transition engine re-derives and validates. Shared by
	// the live hook path (RunBoardFailureWatch) and the durable restart-recovery
	// path (RunBoardReconcile) so both launch the identical review.
	chainReview := func(pmKey string) error {
		return boardTransition(daemon.BoardTransitionRequest{
			PMKey: pmKey,
			From:  daemon.BoardImplementation,
			To:    daemon.BoardVerification,
		})
	}
	// The diagnoser reads the dead run's persisted artifacts so the failed
	// marker says what actually broke instead of the generic stage line.
	diagnoseFailure := func(agentName, hookErrorType string) daemon.FailureDiagnosis {
		d := agent.DiagnoseFailure(agentName, hookErrorType)
		return daemon.FailureDiagnosis{Headline: d.Headline, Detail: d.Detail}
	}
	// A daemon only chains a review for a handoff branch it can resolve on its
	// own machine — a board-context fix leaves its branch local on the machine
	// that produced it, so a daemon elsewhere leaves the handoff for one that can
	// reach it (SC-652). The board operates on the single registered project.
	branchReachable := func(branch string) bool {
		return boardBranchReachable(ctx, ds.srv.Projects, branch)
	}
	// A handoff must name commits the branch actually contains — a retry that never
	// pushed its work named SHAs no machine could see (735). This gate verifies
	// every named commit is reachable from the branch on this machine (local ref or
	// origin/<branch>); any absent commit fails the check.
	commitsPresent := func(branch string, commits []string) bool {
		return boardCommitsPresent(ctx, ds.srv.Projects, branch, commits)
	}
	// A cleanly finished stage is the only thing that authorizes reclaiming the
	// run's private worktree; every other exit keeps the work for forensics
	// (SC-731). MarkHandoff is best-effort/idempotent.
	onHandoff := func(agentName string) { agent.MarkHandoff(agentName) }
	go daemon.RunBoardFailureWatch(ctx, ds.srv.HookEvents,
		boardPMCommenterFunc(ds.srv.Projects, ds.vaultResolver),
		chainReview, branchReachable, commitsPresent, diagnoseFailure, onHandoff, ds.daemonID, logger)
	// The live chain fires only on the one-shot exit hook; this pass re-scans
	// comments to recover a handoff orphaned by a daemon restart or lost hook
	// (SC-430).
	go daemon.RunBoardReconcile(ctx,
		boardReconcileListerFunc(ds.srv.Projects, ds.vaultResolver),
		branchReachable, commitsPresent, chainReview, daemon.BoardReconcileInterval, logger)

	return ds.srv.ListenAndServe(ctx)
}

// startChromeServices launches the socket relay and Chrome MCP proxy.
func startChromeServices(ctx context.Context, chromeAddr, token string, logger zerolog.Logger) {
	socketDir, sdErr := chrome.SocketDir()
	if sdErr != nil {
		logger.Warn().Err(sdErr).Msg("resolving socket directory")
		return
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
		Logger: logger,
	}

	go func() {
		if err := chromeSrv.ListenAndServe(ctx); err != nil {
			logger.Error().Err(err).Msg("chrome proxy server failed")
		}
	}()
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

			if !cmd.Flags().Changed("addr") {
				if info, err := daemon.ReadInfo(); err == nil {
					addr = info.Addr
				}
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
func buildProxyServer(addr string, interactive bool, logger zerolog.Logger, emitter proxy.NetworkEventEmitter) (*proxy.Server, string, error) {
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

	interceptor, interceptStatus := buildInterceptor(proxyCfg, logger)
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
func buildInterceptor(proxyCfg *proxy.Config, logger zerolog.Logger) (proxy.Interceptor, string) {
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
}

func fetchTrackerIssuesFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) func() ([]daemon.TrackerIssuesResult, error) {
	return func() ([]daemon.TrackerIssuesResult, error) {
		jobs, results, err := listTrackerIssues(reg, resolver)
		if err != nil {
			return nil, err
		}

		// Scan PM-tracker comments for [human:ready-for-review] handoffs and
		// per-PM board state, then propagate them onto the results. See
		// cli/CLAUDE.md "Review handoff".
		readyKeys, readyPRs, boardCards := scanReadyForReview(jobs, results)
		applyScanResults(results, readyKeys, readyPRs, boardCards)
		return results, nil
	}
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
		entries := reg.Entries()
		if len(entries) == 0 {
			return nil, errors.WithDetails("no project registered for issue detail")
		}
		entry := entries[0]
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

// listTrackerIssues collects every (instance, project) pair from the registry and
// fetches their open issues in parallel (Phase 1). It returns the jobs aligned 1:1
// with the results so a later comment scan can recover each result's provider
// without re-loading instances from disk.
func listTrackerIssues(reg *daemon.ProjectRegistry, resolver *vault.Resolver) ([]fetchJob, []daemon.TrackerIssuesResult, error) {
	// Collect all (instance, project) pairs first.
	var jobs []fetchJob
	for _, entry := range reg.Entries() {
		instances, err := cmdutil.LoadAllInstancesWithResolver(entry.Dir, entry.EnvLookup(), resolver)
		if err != nil {
			return nil, nil, err
		}
		for _, inst := range instances {
			projects := inst.Projects
			if len(projects) == 0 {
				projects = []string{""}
			}
			for _, p := range projects {
				jobs = append(jobs, fetchJob{inst: inst, project: p})
			}
		}
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
			issues, fetchErr := job.inst.Provider.ListIssues(ctx, tracker.ListOptions{
				Project: job.project,
				// A ticket the board cannot fetch is a ticket silently lost —
				// the cap must comfortably exceed any real open backlog. The
				// per-ticket comment scan this once bounded stays cheap: idea
				// tickets skip it entirely and the rest fan out concurrently.
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
				Issues:      issues,
			}
			if fetchErr != nil {
				results[i].Err = fetchErr.Error()
			}
		}(i, job)
	}
	wg.Wait()
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
func scanReadyForReview(jobs []fetchJob, results []daemon.TrackerIssuesResult) (map[string]bool, map[string]string, map[string]daemon.BoardCard) {
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
}

func (l dockerAgentLauncher) Launch(ctx context.Context, name, prompt, workspace, configDir string) error {
	docker, err := devcontainer.NewDockerClient()
	if err != nil {
		return errors.WrapWithDetails(err, "connecting to Docker for board agent", "agent", name)
	}
	defer func() { _ = docker.Close() }()

	mgr := &agent.Manager{Docker: docker}
	_, err = mgr.Start(ctx, agent.StartOpts{
		Name:      name,
		Prompt:    prompt,
		SkipPerms: true,
		Workspace: workspace,
		ConfigDir: configDir,
		DaemonID:  l.daemonID,
	})
	return err
}

// boardProjectDir resolves the single registered project's directory, the repo
// the board's git probes run against. ok is false when no project is registered.
func boardProjectDir(projects *daemon.ProjectRegistry) (string, bool) {
	if projects == nil {
		return "", false
	}
	entries := projects.Entries()
	if len(entries) == 0 {
		return "", false
	}
	return entries[0].Dir, true
}

// boardBranchReachable reports whether a handoff branch resolves on this machine
// (local ref or origin) — a board-context fix leaves its branch local on the
// machine that produced it (SC-652). A 15s timeout bounds the git probe.
func boardBranchReachable(ctx context.Context, projects *daemon.ProjectRegistry, branch string) bool {
	dir, ok := boardProjectDir(projects)
	if !ok {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return gitrepo.BranchReachable(probeCtx, dir, branch)
}

// boardCommitsPresent reports whether every named commit is reachable from
// branch on this machine — the gate that keeps a handoff from naming SHAs no
// machine could see (735). Any absent commit fails the check. A 15s timeout
// bounds the git probes.
func boardCommitsPresent(ctx context.Context, projects *daemon.ProjectRegistry, branch string, commits []string) bool {
	dir, ok := boardProjectDir(projects)
	if !ok {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for _, sha := range commits {
		if !gitrepo.CommitReachable(probeCtx, dir, branch, sha) {
			return false
		}
	}
	return true
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
	pr, err := creator.CreatePullRequest(ctx, &forge.PullRequest{
		Repo:  repo,
		Base:  base,
		Head:  req.Branch,
		Title: req.Title,
		Body:  req.Body,
	})
	if err != nil {
		return daemon.PRResult{}, errors.WrapWithDetails(err, "creating pull request", "repo", repo, "head", req.Branch)
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
	remoteSHA, err := gitrepo.RevParse(ctx, dir, "origin/"+branch)
	if err != nil {
		return err
	}
	return gitrepo.PushWithLease(ctx, dir, branch, remoteSHA)
}

// EnsureMergeable makes the handoff branch current with the base before the
// deploy attempts the merge: it fetches the base, and when the branch does not
// already contain the base tip it rebases the branch onto origin/<base>,
// re-pushes (lease when the branch is on origin), and re-verifies. A rebase error
// is a real conflict the mechanical path cannot resolve — the deploy must fail
// loudly rather than merge blind (735).
func (p forgeDeployer) EnsureMergeable(ctx context.Context, req daemon.PRRequest) error {
	dir, branch := req.WorkspaceDir, req.Branch
	base := gitrepo.DefaultBranch(ctx, dir)
	if err := gitrepo.Fetch(ctx, dir, base); err != nil {
		return err
	}
	originBase := "origin/" + base
	// Already current: the branch contains the base tip, so its PR is mergeable
	// without touching it.
	if gitrepo.IsAncestor(ctx, dir, originBase, branch) {
		return nil
	}
	// Record the remote tip so the post-rebase push can lease against it — only
	// meaningful when the branch is already on origin.
	var remoteSHA string
	onOrigin := gitrepo.BranchExistsRemote(ctx, dir, branch)
	if onOrigin {
		sha, err := gitrepo.RevParse(ctx, dir, "origin/"+branch)
		if err != nil {
			return err
		}
		remoteSHA = sha
	}
	if err := gitrepo.Rebase(ctx, dir, originBase, branch); err != nil {
		return err
	}
	if onOrigin {
		if err := gitrepo.PushWithLease(ctx, dir, branch, remoteSHA); err != nil {
			return err
		}
	} else if err := gitrepo.Push(ctx, dir, branch); err != nil {
		return err
	}
	// A clean rebase that still does not contain the base tip means the branch
	// could not be made mergeable — surface it rather than merge into a conflict.
	if !gitrepo.IsAncestor(ctx, dir, originBase, branch) {
		return errors.WithDetails("branch still not mergeable after rebase", "branch", branch, "base", base)
	}
	return nil
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

// resolveForge finds the configured instance that carries a forge capability
// for the workspace and resolves the "owner/repo" from origin.
func resolveForge(dir string, lookup config.EnvLookup, resolver *vault.Resolver) (forge.Creator, string, error) {
	instances, err := cmdutil.LoadAllInstancesWithResolver(dir, lookup, resolver)
	if err != nil {
		return nil, "", err
	}
	var creator forge.Creator
	for _, inst := range instances {
		if inst.Forge != nil || forge.IsForgeKind(inst.Kind) {
			if inst.Forge != nil {
				creator = inst.Forge
				break
			}
		}
	}
	if creator == nil {
		return nil, "", errors.WithDetails("no forge configured for workspace", "dir", dir)
	}

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

// closeTicketerFunc builds the daemon's CloseTicketer closure: it resolves the
// PM transitioner by role per request and moves the ticket to its Done status.
// "done" is the status CATEGORY, not a literal label — the tracker resolves it
// to the workflow's done state, the same convention `issue start` uses with
// "started", so no team-specific status name is hardcoded.
func closeTicketerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) func(daemon.CloseTicketRequest) error {
	return func(req daemon.CloseTicketRequest) error {
		entries := reg.Entries()
		if len(entries) == 0 {
			return errors.WithDetails("no project registered for close ticket")
		}
		entry := entries[0]
		lookup := entry.EnvLookup()
		transitioner, err := resolvePMTransitioner(entry.Dir, lookup, resolver)
		if err != nil {
			return err
		}
		return transitioner.TransitionIssue(context.Background(), req.PMKey, "done")
	}
}

// boardTransitionDepsFor resolves the transition engine's collaborators for
// the single registered project: the PM commenter by role, the Docker launcher
// and the forge publisher against the resolved project dir. Shared by the
// board-transition and board-fix closures so both routes drive the exact same
// engine.
func boardTransitionDepsFor(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string, logger zerolog.Logger) (daemon.BoardTransitionDeps, error) {
	entries := reg.Entries()
	if len(entries) == 0 {
		return daemon.BoardTransitionDeps{}, errors.WithDetails("no project registered for board transition")
	}
	entry := entries[0]
	lookup := entry.EnvLookup()
	commenter, err := resolvePMCommenter(entry.Dir, lookup, resolver)
	if err != nil {
		return daemon.BoardTransitionDeps{}, err
	}
	return daemon.BoardTransitionDeps{
		Commenter: commenter,
		Launcher:  dockerAgentLauncher{daemonID: daemonID},
		Deployer:  forgeDeployer{resolver: resolver, lookup: lookup},
		CloseTicket: func(pmKey string) error {
			transitioner, err := resolvePMTransitioner(entry.Dir, lookup, resolver)
			if err != nil {
				return err
			}
			return transitioner.TransitionIssue(context.Background(), pmKey, "done")
		},
		WorkspaceDir: entry.Dir,
		ConfigDir:    entry.Dir,
		DaemonID:     daemonID,
		Logger:       logger,
	}, nil
}

// boardTransitionerFunc builds the daemon's BoardTransitioner closure: it
// resolves the PM commenter by role per request and applies the transition with
// the Docker launcher and forge publisher against the resolved project dir.
func boardTransitionerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string, logger zerolog.Logger) func(daemon.BoardTransitionRequest) error {
	return func(req daemon.BoardTransitionRequest) error {
		deps, err := boardTransitionDepsFor(reg, resolver, daemonID, logger)
		if err != nil {
			return err
		}
		return deps.ApplyTransition(context.Background(), req)
	}
}

// boardFixerFunc builds the daemon's BoardFixer closure: same collaborators as
// a board transition, but the entry point is the autonomous bug-fix pipeline
// (planning gate skipped — autofix triages, plans and fixes in one run).
// boardOptionerFunc builds the daemon's BoardOptioner closure: it records a
// chosen option and relaunches the block's stage with the choice injected.
func boardOptionerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string, logger zerolog.Logger) func(daemon.BoardOptionRequest) error {
	return func(req daemon.BoardOptionRequest) error {
		deps, err := boardTransitionDepsFor(reg, resolver, daemonID, logger)
		if err != nil {
			return err
		}
		return deps.ApplyOption(context.Background(), req)
	}
}

func boardFixerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver, daemonID string, logger zerolog.Logger) func(daemon.BoardFixRequest) error {
	return func(req daemon.BoardFixRequest) error {
		deps, err := boardTransitionDepsFor(reg, resolver, daemonID, logger)
		if err != nil {
			return err
		}
		return deps.ApplyFix(context.Background(), req)
	}
}

// bugCreatorFunc builds the daemon's BugCreator closure: it files a bug-typed
// ticket on the role-resolved PM tracker. The provider maps the bug type onto
// its native defect marker (issue/story type where one exists, the bug label
// otherwise), so the Bugs pane recognises the card on every backend.
func bugCreatorFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) func(daemon.BugCreateRequest) (daemon.BugCreateResponse, error) {
	return func(req daemon.BugCreateRequest) (daemon.BugCreateResponse, error) {
		if err := daemon.ValidateBugCreate(req); err != nil {
			return daemon.BugCreateResponse{}, err
		}
		entries := reg.Entries()
		if len(entries) == 0 {
			return daemon.BugCreateResponse{}, errors.WithDetails("no project registered for bug creation")
		}
		entry := entries[0]
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
		return daemon.BugCreateResponse{Key: created.Key, URL: created.URL}, nil
	}
}

// featuresGeneratorFunc builds the daemon's FeaturesGenerator closure: it
// launches the human-features skill in the registered project's devcontainer,
// exactly like a board stage transition, so the desktop Features pane's
// Generate/Refresh button reuses the same containerized agent path.
func featuresGeneratorFunc(reg *daemon.ProjectRegistry) func() error {
	return func() error {
		entries := reg.Entries()
		if len(entries) == 0 {
			return errors.WithDetails("no project registered for feature generation")
		}
		entry := entries[0]
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

// mockupsCreatorFunc builds the daemon's MockupsCreator closure: it records
// the ticket→mockup-set link in the project's .human/mockups.json and launches
// the human-mockups skill in the registered project's devcontainer — the same
// containerized agent path as feature generation. The link is written BEFORE
// the launch (it doubles as the board's "creating…" marker) and rolled back if
// the launch fails, so the menu never sticks on a set that was never started.
func mockupsCreatorFunc(reg *daemon.ProjectRegistry) func(daemon.CreateMocksRequest) error {
	return func(req daemon.CreateMocksRequest) error {
		entries := reg.Entries()
		if len(entries) == 0 {
			return errors.WithDetails("no project registered for mock creation")
		}
		entry := entries[0]
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
	entries := r.reg.Entries()
	if len(entries) == 0 {
		return daemon.IdeationTurn{}, errors.WithDetails("no project registered for ideation")
	}
	// Read-only tool allowlist: the agent may inspect the repo but nothing
	// else; the daemon, not the agent, writes the ticket. Single argv element
	// so the variadic flag cannot swallow the positional prompt.
	args := []string{"-p", prompt, "--output-format", "json", "--allowedTools", "Read Grep Glob"}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	cmd := exec.CommandContext(ctx, "claude", args...) // #nosec G204 -- fixed binary, prompt is a discrete argv element
	cmd.Dir = entries[0].Dir
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
		project := ""
		if len(inst.Projects) > 0 {
			project = inst.Projects[0]
		}
		return c, project, nil
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
func ideationEngine(reg *daemon.ProjectRegistry, resolver *vault.Resolver, hookStore *daemon.HookEventStore, logger zerolog.Logger) *daemon.IdeationEngine {
	firstEntry := func() (daemon.ProjectEntry, error) {
		entries := reg.Entries()
		if len(entries) == 0 {
			return daemon.ProjectEntry{}, errors.WithDetails("no project registered for ideation")
		}
		return entries[0], nil
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
		Logger: logger,
	}
}

// boardPMCommenterFunc resolves the PM commenter for the board failure watcher.
func boardPMCommenterFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) func() (tracker.Commenter, error) {
	return func() (tracker.Commenter, error) {
		entries := reg.Entries()
		if len(entries) == 0 {
			return nil, errors.WithDetails("no project registered for board failure watch")
		}
		entry := entries[0]
		return resolvePMCommenter(entry.Dir, entry.EnvLookup(), resolver)
	}
}

// boardReconcileListerFunc enumerates open PM cards with their comment threads
// for the durable reconcile pass. It reuses the listTrackerIssues fan-out, then
// fetches each open PM ticket's comments (skipping ideas, which carry no
// pipeline markers) — mirroring scanReadyForReview's fan-out without altering
// it. Best-effort: a per-ticket error drops that ticket, not the whole tick, so
// one flaky tracker call never blocks recovery of the rest.
func boardReconcileListerFunc(reg *daemon.ProjectRegistry, resolver *vault.Resolver) daemon.ReconcileLister {
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
