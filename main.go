package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/StephanSchmidt/human/cmd/cmdagent"
	"github.com/StephanSchmidt/human/cmd/cmdamplitude"
	"github.com/StephanSchmidt/human/cmd/cmdauto"
	"github.com/StephanSchmidt/human/cmd/cmdbrowser"
	"github.com/StephanSchmidt/human/cmd/cmdclickup"
	"github.com/StephanSchmidt/human/cmd/cmddaemon"
	"github.com/StephanSchmidt/human/cmd/cmdfigma"
	"github.com/StephanSchmidt/human/cmd/cmdindex"
	"github.com/StephanSchmidt/human/cmd/cmdinit"
	"github.com/StephanSchmidt/human/cmd/cmdnotion"
	"github.com/StephanSchmidt/human/cmd/cmdping"
	"github.com/StephanSchmidt/human/cmd/cmdprovider"
	"github.com/StephanSchmidt/human/cmd/cmdproxy"
	"github.com/StephanSchmidt/human/cmd/cmdslack"
	"github.com/StephanSchmidt/human/cmd/cmdtelegram"
	"github.com/StephanSchmidt/human/cmd/cmdtracker"
	"github.com/StephanSchmidt/human/cmd/cmdtui"
	"github.com/StephanSchmidt/human/cmd/cmdusage"
	"github.com/StephanSchmidt/human/cmd/cmdutil"
	"github.com/StephanSchmidt/human/errors"
	"github.com/StephanSchmidt/human/internal/claude"
	"github.com/StephanSchmidt/human/internal/config"
	"github.com/StephanSchmidt/human/internal/daemon"
	"github.com/StephanSchmidt/human/internal/tracker"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// helpInstanceLoader is the function used by the root help template to load
// tracker instances.  It defaults to LoadAllInstances(DirCwd) and can be
// overridden in tests.
var helpInstanceLoader = func() ([]tracker.Instance, error) {
	return cmdutil.LoadAllInstances(config.DirCwd)
}

// autoInstanceLoader is used by auto-detect commands to load tracker instances.
// It defaults to LoadAllInstances(DirCwd) and can be overridden in tests.
var autoInstanceLoader = func() ([]tracker.Instance, error) {
	return cmdutil.LoadAllInstances(config.DirCwd)
}

// --- newRootCmd builds the Cobra command tree ---

func newRootCmd() *cobra.Command {
	deps := cmdutil.DefaultDeps()

	// autoDeps uses the package-level autoInstanceLoader so tests can
	// inject mock instances without touching the real config path.
	// Both LoadInstances and LoadInstancesCtx must be overridden so the
	// context-aware resolve path also picks up the mock loader.
	autoDeps := deps
	autoDeps.LoadInstances = func(_ string) ([]tracker.Instance, error) {
		return autoInstanceLoader()
	}
	autoDeps.LoadInstancesCtx = func(_ context.Context, _ string) ([]tracker.Instance, error) {
		return autoInstanceLoader()
	}

	rootCmd := &cobra.Command{
		Use:   "human",
		Short: "Unified CLI for issue trackers and tools",
		Long: `Unified CLI to list, read, create, delete, and comment on issues
across Jira, GitHub, GitLab, Linear, Azure DevOps, Shortcut, and ClickUp.
Search and read content from Notion workspaces. Browse Figma designs.
Queries Amplitude product analytics. Reads Telegram bot messages.

Use it to:
  - fetch a ticket before planning implementation
  - check what issues exist in a project
  - search across all trackers with a local index
  - create tickets for bugs or features you discover
  - add comments with status updates or findings
  - look up ticket details (status, assignee, description)
  - search Notion for meeting notes, specs, and docs
  - browse Figma files, components, and comments
  - query Amplitude events, funnels, retention, and cohorts
  - read pending Telegram bot messages

All trackers share the same command structure:
  human <tracker> issues list   — JSON array of issues
  human <tracker> issue  get    — single issue as markdown
  human <tracker> issue  create — create and return key
  human <tracker> issue  edit   — update title and/or description
  human <tracker> issue  start  — transition + assign to yourself
  human <tracker> issue  delete — delete or close
  human <tracker> issue  statuses — list available statuses
  human <tracker> issue  status   — set issue status
  human <tracker> issue  comment add/list — manage comments

Tools:
  human notion search QUERY     — search Notion workspace
  human notion page get ID      — page content as markdown
  human notion database query ID — query database rows
  human notion databases list   — list shared databases
  human figma file get KEY      — file metadata and pages
  human figma file comments KEY — design feedback
  human figma file components KEY — published components
  human amplitude events list   — event types with active users
  human amplitude cohorts list  — behavioral cohorts
  human telegram list            — pending bot messages
  human telegram get UPDATE_ID   — specific message details

Configure trackers and tools in .humanconfig.yaml or pass credentials via flags/env vars.`,
		Version: version + " (" + commit + ") " + date,
		// When no subcommand is given, show help.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		SilenceUsage: true,
	}

	// Override help to append examples and connected trackers.
	// Wrap helpInstanceLoader in a closure so tests can override it after
	// newRootCmd() returns.
	cmdutil.SetupHelp(rootCmd, func() ([]tracker.Instance, error) {
		return helpInstanceLoader()
	})

	// Global persistent flags.
	pf := rootCmd.PersistentFlags()
	pf.String("tracker", "", "Named tracker instance from .humanconfig")
	pf.Bool("safe", os.Getenv("HUMAN_SAFE") == "1", "Block destructive operations (deletes)")

	// Credential flags — functional but hidden from help (use env vars or .humanconfig).
	credFlags := []struct{ name, env, help string }{
		{"jira-key", "JIRA_KEY", "Jira API token"},
		{"jira-url", "JIRA_URL", "Jira base URL"},
		{"jira-user", "JIRA_USER", "Jira user email"},
		{"github-token", "GITHUB_TOKEN", "GitHub personal access token"},
		{"github-url", "GITHUB_URL", "GitHub API base URL"},
		{"gitlab-token", "GITLAB_TOKEN", "GitLab private token"},
		{"gitlab-url", "GITLAB_URL", "GitLab base URL"},
		{"linear-token", "LINEAR_TOKEN", "Linear API key"},
		{"linear-url", "LINEAR_URL", "Linear API base URL"},
		{"azure-token", "AZURE_TOKEN", "Azure DevOps PAT token"},
		{"azure-url", "AZURE_URL", "Azure DevOps base URL"},
		{"azure-org", "AZURE_ORG", "Azure DevOps organization"},
		{"shortcut-token", "SHORTCUT_TOKEN", "Shortcut API token"},
		{"shortcut-url", "SHORTCUT_URL", "Shortcut API base URL"},
		{"clickup-token", "CLICKUP_TOKEN", "ClickUp personal API token"},
		{"clickup-url", "CLICKUP_URL", "ClickUp API base URL"},
	}
	for _, f := range credFlags {
		pf.String(f.name, os.Getenv(f.env), f.help)
		_ = pf.MarkHidden(f.name)
	}

	// --- Command groups ---
	rootCmd.AddGroup(
		&cobra.Group{ID: "shortcuts", Title: "Quick Commands:"},
		&cobra.Group{ID: "trackers", Title: "Issue Trackers:"},
		&cobra.Group{ID: "tools", Title: "Tools:"},
		&cobra.Group{ID: "utility", Title: "Utility:"},
	)

	// Hide the auto-generated completion command.
	rootCmd.CompletionOptions.HiddenDefaultCmd = true

	// --- Quick commands (auto-detect tracker) ---
	autoGetCmd := cmdauto.BuildAutoGetCmd(autoDeps)
	autoGetCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoGetCmd)

	autoListCmd := cmdauto.BuildAutoListCmd(autoDeps)
	autoListCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoListCmd)

	autoStatusesCmd := cmdauto.BuildAutoStatusesCmd(autoDeps)
	autoStatusesCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoStatusesCmd)

	autoStatusCmd := cmdauto.BuildAutoStatusCmd(autoDeps)
	autoStatusCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoStatusCmd)

	// --- Provider commands (dynamic registration) ---
	providers := []string{"jira", "github", "gitlab", "linear", "azuredevops", "shortcut", "clickup"}
	for _, kind := range providers {
		providerCmd := &cobra.Command{
			Use:     kind,
			Short:   kind + " issue tracker",
			GroupID: "trackers",
		}
		for _, sub := range cmdprovider.BuildProviderCommands(kind, deps) {
			providerCmd.AddCommand(sub)
		}
		// Add ClickUp-specific commands (hierarchy browsing, custom fields, members).
		if kind == "clickup" {
			for _, sub := range cmdclickup.BuildClickUpCommands(deps) {
				providerCmd.AddCommand(sub)
			}
		}
		rootCmd.AddCommand(providerCmd)
	}

	// --- Notion (tools) ---
	notionCmd := cmdnotion.BuildNotionCommands()
	notionCmd.GroupID = "tools"
	rootCmd.AddCommand(notionCmd)

	// --- Figma (tools) ---
	figmaCmd := cmdfigma.BuildFigmaCommands()
	figmaCmd.GroupID = "tools"
	rootCmd.AddCommand(figmaCmd)

	// --- Amplitude (tools) ---
	amplitudeCmd := cmdamplitude.BuildAmplitudeCommands()
	amplitudeCmd.GroupID = "tools"
	rootCmd.AddCommand(amplitudeCmd)

	// --- Telegram (tools) ---
	telegramCmd := cmdtelegram.BuildTelegramCommands()
	telegramCmd.GroupID = "tools"
	rootCmd.AddCommand(telegramCmd)

	slackCmd := cmdslack.BuildSlackCommands()
	slackCmd.GroupID = "tools"
	rootCmd.AddCommand(slackCmd)

	// --- Static commands ---
	trackerCmd := cmdtracker.BuildTrackerCmd(cmdutil.LoadAllInstances)
	trackerCmd.GroupID = "utility"
	rootCmd.AddCommand(trackerCmd)

	installCmd := buildInstallCmd()
	installCmd.GroupID = "utility"
	rootCmd.AddCommand(installCmd)

	daemonCmd := cmddaemon.BuildDaemonCmd(newRootCmd, version)
	daemonCmd.GroupID = "utility"
	rootCmd.AddCommand(daemonCmd)

	browserCmd := cmdbrowser.BuildBrowserCmd()
	browserCmd.GroupID = "utility"
	rootCmd.AddCommand(browserCmd)

	initCmd := cmdinit.BuildInitCmd()
	initCmd.GroupID = "utility"
	rootCmd.AddCommand(initCmd)

	chromeBridgeCmd := cmddaemon.BuildChromeBridgeCmd(version)
	chromeBridgeCmd.GroupID = "utility"
	rootCmd.AddCommand(chromeBridgeCmd)

	usageCmd := cmdusage.BuildUsageCmd()
	usageCmd.GroupID = "utility"
	rootCmd.AddCommand(usageCmd)

	indexDeps := cmdindex.DefaultIndexDeps()
	indexCmd := cmdindex.BuildIndexCmd(indexDeps)
	indexCmd.GroupID = "utility"
	rootCmd.AddCommand(indexCmd)

	searchCmd := cmdindex.BuildSearchCmd(indexDeps)
	searchCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(searchCmd)

	tuiCmd := cmdtui.BuildTuiCmd()
	tuiCmd.GroupID = "utility"
	rootCmd.AddCommand(tuiCmd)

	agentCmd := cmdagent.BuildAgentCmd()
	agentCmd.GroupID = "utility"
	rootCmd.AddCommand(agentCmd)

	pingCmd := cmdping.BuildPingCmd()
	pingCmd.GroupID = "utility"
	rootCmd.AddCommand(pingCmd)

	proxyCmd := cmdproxy.BuildProxyCmd()
	proxyCmd.GroupID = "utility"
	rootCmd.AddCommand(proxyCmd)

	// hook reads Claude Code hook JSON from stdin, extracts event fields,
	// and forwards them to the daemon as hook-event args. Runs locally
	// (listed in isLocalSubcommand) so stdin is available.
	hookCmd := &cobra.Command{
		Use:    "hook",
		Short:  "Forward a Claude Code hook event (reads JSON from stdin)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE:   buildHookRunE(),
	}
	rootCmd.AddCommand(hookCmd)

	// hook-event is kept for backwards compatibility with older hook scripts.
	// When the daemon is running, isLocalSubcommand returns false so it is
	// forwarded to the daemon where routeIntercept handles it.
	hookEventCmd := &cobra.Command{
		Use:    "hook-event [event] [session-id] [cwd] [notification-type]",
		Short:  "Send a Claude Code hook event to the daemon (legacy)",
		Hidden: true,
		Args:   cobra.MaximumNArgs(4),
		RunE: func(_ *cobra.Command, _ []string) error {
			return nil // no-op when daemon is not running
		},
	}
	rootCmd.AddCommand(hookEventCmd)

	return rootCmd
}

func buildInstallCmd() *cobra.Command {
	var agent string
	var personal bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install agent integrations",
		RunE: func(_ *cobra.Command, _ []string) error {
			switch agent {
			case "claude":
				fmt.Println("Installing Claude Code files...")
				if err := claude.Install(os.Stdout, claude.OSFileWriter{}, personal); err != nil {
					return err
				}
				fmt.Println("Done. Skill: /human-plan <ticket-key>")
			default:
				return errors.WithDetails("unsupported agent", "agent", agent)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "Agent to install (claude)")
	_ = cmd.MarkFlagRequired("agent")
	cmd.Flags().BoolVar(&personal, "personal", false, "Install to ~/.claude/ (personal) instead of .claude/ (project)")
	return cmd
}

// hookInput is the JSON structure Claude Code sends to hook scripts via stdin.
type hookInput struct {
	EventName        string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	Cwd              string `json:"cwd"`
	NotificationType string `json:"notification_type"`
	ToolName         string `json:"tool_name"`
	ErrorType        string `json:"error"`
}

// buildHookRunE returns the RunE for the "hook" command. It reads Claude Code
// hook JSON from stdin and forwards it to the daemon as hook-event args.
func buildHookRunE() func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil // stdin unavailable — silently ignore
		}
		var input hookInput
		if err := json.Unmarshal(data, &input); err != nil {
			return nil // malformed JSON — silently ignore
		}
		if input.EventName == "" {
			return nil
		}

		// Forward to daemon if available.
		addr := os.Getenv("HUMAN_DAEMON_ADDR")
		token := os.Getenv("HUMAN_DAEMON_TOKEN")
		if addr == "" {
			addr, token = discoverDaemon(token)
		}
		if addr == "" {
			return nil // no daemon — silently ignore
		}

		agentName := os.Getenv("HUMAN_AGENT_NAME")
		args := []string{"hook-event", input.EventName, input.SessionID, input.Cwd, input.NotificationType, input.ToolName, input.ErrorType, agentName}
		_, _ = daemon.RunRemote(addr, token, args, version)
		return nil
	}
}

// localSubcommands lists commands that must execute locally rather than
// being forwarded to the daemon.
var localSubcommands = map[string]bool{
	"daemon":        true,
	"chrome-bridge": true,
	"install":       true,
	"init":          true,
	"usage":         true,
	"index":         true,
	"tui":           true,
	"ping":          true,
	"proxy":         true,
	"hook":          true,
	"agent":         true,
}

// globalValueFlags lists global persistent flags that take a value. When these
// appear in space-separated form (e.g. "--tracker work"), the value token must
// be skipped so it isn't mistaken for the subcommand name. Keep this list in
// sync with PersistentFlags() in newRootCmd.
var globalValueFlags = map[string]bool{
	"--tracker":        true,
	"--jira-key":       true,
	"--jira-url":       true,
	"--jira-user":      true,
	"--github-token":   true,
	"--github-url":     true,
	"--gitlab-token":   true,
	"--gitlab-url":     true,
	"--linear-token":   true,
	"--linear-url":     true,
	"--azure-token":    true,
	"--azure-url":      true,
	"--azure-org":      true,
	"--shortcut-token": true,
	"--shortcut-url":   true,
	"--clickup-token":  true,
	"--clickup-url":    true,
}

// isLocalSubcommand returns true if args represent a command that must
// execute locally rather than being forwarded to the daemon. It understands
// space-separated value-taking flags (e.g. "--tracker work daemon") so the
// value token is not mistaken for the subcommand.
func isLocalSubcommand(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return false
		}
		// --version should always run locally to show the client's version.
		if a == "--version" || a == "-v" {
			return true
		}
		if len(a) > 0 && a[0] == '-' {
			// Skip the value of a known value-taking flag in its space-separated
			// form. The "--flag=value" form is a single token and needs no skip.
			if globalValueFlags[a] && i+1 < len(args) {
				i++
			}
			continue
		}
		return localSubcommands[a]
	}
	return false
}

// --- main ---

// subcmdFromBinary checks whether the binary was invoked via a symlink
// like "human-browser" and returns the implied subcommand (e.g. "browser").
// Returns "" when os.Args[0] is just "human" or unrecognised.
func subcmdFromBinary() string {
	base := filepath.Base(os.Args[0]) //nolint:nilaway // os.Args is always set in main
	// Strip common extensions (.exe on Windows).
	base = strings.TrimSuffix(base, ".exe")
	if strings.HasPrefix(base, "human-") {
		return base[len("human-"):]
	}
	return ""
}

// discoverDaemon auto-discovers daemon address and token from the info file,
// and propagates chrome/proxy addresses into environment variables.
// Falls back to probing host.docker.internal for cross-container discovery.
func discoverDaemon(token string) (string, string) {
	info, err := daemon.ReadInfo()
	if err == nil && info.IsReachable() {
		return applyDaemonInfo(info, token)
	}

	// Fallback: probe host.docker.internal at well-known ports.
	// This enables discovery inside Docker containers without env vars.
	fallback := daemon.DaemonInfo{
		Addr:       fmt.Sprintf("%s:%d", daemon.DockerHost, daemon.DefaultPort),
		ChromeAddr: fmt.Sprintf("%s:%d", daemon.DockerHost, daemon.DefaultChromePort),
		ProxyAddr:  fmt.Sprintf("%s:%d", daemon.DockerHost, daemon.DefaultProxyPort),
	}
	if fallback.IsReachable() {
		// Use token from daemon.json if available (e.g. via volume mount).
		if err == nil && token == "" {
			fallback.Token = info.Token
		}
		return applyDaemonInfo(fallback, token)
	}

	return "", token
}

// applyDaemonInfo propagates chrome/proxy addresses into environment variables
// and returns the daemon address and token.
func applyDaemonInfo(info daemon.DaemonInfo, token string) (string, string) {
	if token == "" {
		token = info.Token
	}
	if os.Getenv("HUMAN_CHROME_ADDR") == "" && info.ChromeAddr != "" {
		if err := os.Setenv("HUMAN_CHROME_ADDR", info.ChromeAddr); err != nil {
			errors.LogError(err).Msg("failed to set HUMAN_CHROME_ADDR")
		}
	}
	if os.Getenv("HUMAN_PROXY_ADDR") == "" && info.ProxyAddr != "" {
		if err := os.Setenv("HUMAN_PROXY_ADDR", info.ProxyAddr); err != nil {
			errors.LogError(err).Msg("failed to set HUMAN_PROXY_ADDR")
		}
	}
	return info.Addr, token
}

func main() {
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	// Busybox-style dispatch: "human-browser URL" → "human browser URL".
	args := os.Args[1:] //nolint:nilaway // os.Args is always set in main
	if sub := subcmdFromBinary(); sub != "" {
		args = append([]string{sub}, args...)
	}

	// Client mode: forward to daemon if configured.
	// Skip forwarding for "daemon" subcommands — they must run locally.
	addr := os.Getenv("HUMAN_DAEMON_ADDR")
	token := os.Getenv("HUMAN_DAEMON_TOKEN")

	// When addr is set via env but token isn't, read token from daemon.json
	// (available inside devcontainers via the ~/.human volume mount).
	if addr != "" && token == "" {
		if info, infoErr := daemon.ReadInfo(); infoErr == nil {
			token = info.Token
		}
	}

	// Auto-discover from daemon info file when env vars are not set.
	if addr == "" {
		addr, token = discoverDaemon(token)
	}

	if addr != "" && !isLocalSubcommand(args) {
		exitCode, err := daemon.RunRemote(addr, token, args, version)
		if err != nil {
			errors.LogError(err).Msg("remote execution failed")
			os.Exit(1)
		}
		os.Exit(exitCode)
	}

	rootCmd := newRootCmd()
	rootCmd.SetArgs(args)
	if err := rootCmd.Execute(); err != nil {
		errors.LogError(err).Msg("command failed")
		os.Exit(1)
	}
}
