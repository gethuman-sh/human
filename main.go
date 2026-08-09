package main

import (
	"bytes"
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
	"golang.org/x/term"

	"github.com/gethuman-sh/human/cmd/cmdagent"
	"github.com/gethuman-sh/human/cmd/cmdagentcontext"
	"github.com/gethuman-sh/human/cmd/cmdamplitude"
	"github.com/gethuman-sh/human/cmd/cmdaudit"
	"github.com/gethuman-sh/human/cmd/cmdauto"
	"github.com/gethuman-sh/human/cmd/cmdbrowser"
	"github.com/gethuman-sh/human/cmd/cmdbug"
	"github.com/gethuman-sh/human/cmd/cmdcapabilities"
	"github.com/gethuman-sh/human/cmd/cmdclickup"
	"github.com/gethuman-sh/human/cmd/cmdcodenav"
	"github.com/gethuman-sh/human/cmd/cmdcommits"
	"github.com/gethuman-sh/human/cmd/cmdconfig"
	"github.com/gethuman-sh/human/cmd/cmddaemon"
	"github.com/gethuman-sh/human/cmd/cmddeploy"
	"github.com/gethuman-sh/human/cmd/cmddoctor"
	"github.com/gethuman-sh/human/cmd/cmdfigma"
	"github.com/gethuman-sh/human/cmd/cmdforge"
	"github.com/gethuman-sh/human/cmd/cmdfsm"
	"github.com/gethuman-sh/human/cmd/cmdhandoff"
	"github.com/gethuman-sh/human/cmd/cmdindex"
	"github.com/gethuman-sh/human/cmd/cmdinit"
	"github.com/gethuman-sh/human/cmd/cmdmarker"
	"github.com/gethuman-sh/human/cmd/cmdmockups"
	"github.com/gethuman-sh/human/cmd/cmdnotion"
	"github.com/gethuman-sh/human/cmd/cmdping"
	"github.com/gethuman-sh/human/cmd/cmdpipeline"
	"github.com/gethuman-sh/human/cmd/cmdplan"
	"github.com/gethuman-sh/human/cmd/cmdprovider"
	"github.com/gethuman-sh/human/cmd/cmdproxy"
	"github.com/gethuman-sh/human/cmd/cmdslack"
	"github.com/gethuman-sh/human/cmd/cmdstate"
	"github.com/gethuman-sh/human/cmd/cmdstats"
	"github.com/gethuman-sh/human/cmd/cmdtelegram"
	"github.com/gethuman-sh/human/cmd/cmdtracker"
	"github.com/gethuman-sh/human/cmd/cmdunderway"
	"github.com/gethuman-sh/human/cmd/cmdusage"
	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/cliflags"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/update"
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
  human assign KEY              — take ownership only (no status change)
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
	// --yes skips the interactive confirmation for a destructive operation. The
	// daemon injects it after an operator approval when re-executing the command, so it
	// must be accepted by every (current and future) destructive subcommand —
	// hence a single persistent flag here rather than per-command registration.
	pf.Bool("yes", false, "Skip interactive confirmation for destructive operations")
	_ = pf.MarkHidden("yes")

	// Credentials come from .humanconfig (with vault refs) or the
	// <KIND>_<NAME>_TOKEN env convention the config loaders already read — one
	// mechanism that needs no per-provider code. The hidden per-tracker
	// --<kind>-token/--<kind>-url flags that used to sit here were a second,
	// provider-specific implementation of the same thing, and a URL flag paired
	// with a credential is a redirection primitive the daemon should not accept
	// over its socket at all. Adding a tracker must not mean adding flags.

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

	autoAssignCmd := cmdauto.BuildAutoAssignCmd(autoDeps)
	autoAssignCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoAssignCmd)

	autoDoneCmd := cmdauto.BuildAutoDoneCmd(autoDeps)
	autoDoneCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoDoneCmd)

	autoCloseCmd := cmdauto.BuildAutoCloseCmd(autoDeps)
	autoCloseCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoCloseCmd)

	autoIdeaCmd := cmdauto.BuildAutoIdeaCmd(autoDeps)
	autoIdeaCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoIdeaCmd)

	autoLinkCmd := cmdauto.BuildAutoLinkCmd(autoDeps)
	autoLinkCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoLinkCmd)

	autoUnlinkCmd := cmdauto.BuildAutoUnlinkCmd(autoDeps)
	autoUnlinkCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoUnlinkCmd)

	autoPRCmd := cmdauto.BuildAutoPRCreateCmd(autoDeps)
	autoPRCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(autoPRCmd)

	planCmd := cmdplan.BuildPlanCmd(autoDeps)
	planCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(planCmd)

	commitsCmd := cmdcommits.BuildCommitsCmd()
	commitsCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(commitsCmd)

	mockupsCmd := cmdmockups.BuildMockupsCmd()
	mockupsCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(mockupsCmd)

	markerCmd := cmdmarker.BuildMarkerCmd(autoDeps)
	markerCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(markerCmd)

	handoffCmd := cmdhandoff.BuildHandoffCmd(autoDeps)
	handoffCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(handoffCmd)

	bugCmd := cmdbug.BuildBugCmd()
	bugCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(bugCmd)

	securityCmd := cmdbug.BuildSecurityCmd()
	securityCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(securityCmd)

	// Deliberately absent from localSubcommands: "state" is forwarded to the
	// daemon so every agent and container shares one store on the daemon host.
	stateCmd := cmdstate.BuildStateCmd()
	stateCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(stateCmd)

	// The mirror case: "capabilities" describes the caller's own checkout and
	// environment, so it must run locally (see localSubcommands).
	capabilitiesCmd := cmdcapabilities.BuildCapabilitiesCmd()
	capabilitiesCmd.GroupID = "utility"
	rootCmd.AddCommand(capabilitiesCmd)

	pipelineCmd := cmdpipeline.BuildPipelineCmd()
	pipelineCmd.GroupID = "utility"
	rootCmd.AddCommand(pipelineCmd)

	deployCmd := cmddeploy.BuildDeployCmd(autoDeps)
	deployCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(deployCmd)

	underwayCmd := cmdunderway.BuildUnderwayCmd()
	rootCmd.AddCommand(underwayCmd)

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

	// A sibling of tracker, not a subcommand of it: code hosts and issue
	// trackers are separate domains with separate config and separate types
	// ([SC-3876]).
	forgeCmd := cmdforge.BuildForgeCmd(cmdutil.LoadForges)
	forgeCmd.GroupID = "utility"
	rootCmd.AddCommand(forgeCmd)

	configCmd := cmdconfig.BuildConfigCmd()
	configCmd.GroupID = "utility"
	rootCmd.AddCommand(configCmd)

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

	rootCmd.AddCommand(cmdaudit.BuildAuditCmd())
	rootCmd.AddCommand(cmdstats.BuildStatsCmd())

	indexDeps := cmdindex.DefaultIndexDeps()
	indexCmd := cmdindex.BuildIndexCmd(indexDeps)
	indexCmd.GroupID = "utility"
	rootCmd.AddCommand(indexCmd)

	searchCmd := cmdindex.BuildSearchCmd(indexDeps)
	searchCmd.GroupID = "shortcuts"
	rootCmd.AddCommand(searchCmd)

	agentCmd := cmdagent.BuildAgentCmd()
	agentCmd.GroupID = "utility"
	rootCmd.AddCommand(agentCmd)

	pingCmd := cmdping.BuildPingCmd()
	pingCmd.GroupID = "utility"
	rootCmd.AddCommand(pingCmd)

	doctorCmd := cmddoctor.BuildDoctorCmd()
	doctorCmd.GroupID = "utility"
	rootCmd.AddCommand(doctorCmd)

	proxyCmd := cmdproxy.BuildProxyCmd()
	proxyCmd.GroupID = "utility"
	rootCmd.AddCommand(proxyCmd)

	codenavCmd := cmdcodenav.BuildCodenavCmd()
	codenavCmd.GroupID = "utility"
	rootCmd.AddCommand(codenavCmd)

	fsmCmd := cmdfsm.BuildFSMCmd()
	fsmCmd.GroupID = "utility"
	rootCmd.AddCommand(fsmCmd)

	agentContextCmd := cmdagentcontext.BuildAgentContextCmd()
	agentContextCmd.GroupID = "utility"
	rootCmd.AddCommand(agentContextCmd)

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

	// confirm-status reports the decision state of a queued destructive
	// operation. With a daemon running it is forwarded and answered by
	// routeIntercept; this stub only surfaces a useful error daemonless.
	confirmStatusCmd := &cobra.Command{
		Use:    "confirm-status ID",
		Short:  "Show the decision state of a pending destructive operation",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return errors.WithDetails("confirm-status requires a running daemon")
		},
	}
	rootCmd.AddCommand(confirmStatusCmd)

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

	rejectUnknownSubcommands(rootCmd)

	return rootCmd
}

// rejectUnknownSubcommands makes a mistyped or non-existent subcommand fail.
//
// A command group holds subcommands and does no work of its own, and cobra's
// default for one is to accept any positional argument, print the group's help
// on stdout and exit 0. So `human fsm next` — a command that has never existed
// — reads as a success carrying a wall of plausible text. Callers that check
// the exit code see nothing wrong, and an agent acting on a stale instruction
// proceeds believing it consulted the tool. Only the root command rejected the
// unknown word; every group below it swallowed one.
//
// cobra.NoArgs alone does not fix it: cobra returns ErrHelp for a command that
// is not runnable before it ever validates the arguments, so the check would
// never run. The group is therefore given a RunE that prints its help — which
// is what running a group bare should do anyway — and that makes it runnable,
// which is what lets NoArgs report `unknown command "next" for "human fsm"`
// and exit non-zero.
//
// A group that already declares Args or a Run has decided the question for
// itself and is left alone.
func rejectUnknownSubcommands(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		rejectUnknownSubcommands(child)
	}
	if cmd.Runnable() || !cmd.HasSubCommands() || cmd.Args != nil {
		return
	}
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
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
				fmt.Println("Agent commits in this project are attributed to the bot identity from .humanconfig (bot:); re-run this after changing that section.")
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
	EventName        string          `json:"hook_event_name"`
	SessionID        string          `json:"session_id"`
	Cwd              string          `json:"cwd"`
	NotificationType string          `json:"notification_type"`
	ToolName         string          `json:"tool_name"`
	ErrorType        string          `json:"error"`
	ToolInput        json.RawMessage `json:"tool_input"`
	SubagentType     string          `json:"subagent_type"`
	Model            string          `json:"model"`
}

// nestedAttribution is the sub-agent attribution Claude Code puts inside
// tool_input when an agent spawns another. Reading it here, from the raw
// payload, is deliberate: the daemon redacts and caps tool_input at 1 KiB
// before storing it, and on a real dispatch these two keys sit after a
// multi-KiB prompt — so anything that reads the stored column instead sees
// them only on short fixtures (SC-3582).
type nestedAttribution struct {
	SubagentType string `json:"subagent_type"`
	Model        string `json:"model"`
}

// resolveAttribution picks the sub-agent type and model for an event. Agent
// spawns nest both inside tool_input; SessionStart puts model at the top level,
// so the nested value wins only where it is actually present and the top-level
// reading stays a fallback rather than being replaced. The top-level
// subagent_type has no producer in today's Claude Code — it is kept so a future
// promotion of the key needs no change here, not because anything fills it.
func resolveAttribution(in hookInput) (subagentType, model string) {
	subagentType, model = in.SubagentType, in.Model

	if len(bytes.TrimSpace(in.ToolInput)) > 0 {
		var nested nestedAttribution
		// A tool input that is not an object (or is malformed) simply carries no
		// attribution — the top-level reading then stands.
		if err := json.Unmarshal(in.ToolInput, &nested); err == nil {
			if nested.SubagentType != "" {
				subagentType = nested.SubagentType
			}
			if nested.Model != "" {
				model = nested.Model
			}
		}
	}

	// A spawn that named no model ran on its parent's. Recording that as a fact
	// is what keeps it answerable apart from an event that never carried an
	// attribution at all (SC-3582).
	if subagentType != "" && model == "" {
		model = hookevents.ModelInherited
	}
	return subagentType, model
}

// hookDeliverer forwards resolved hook-event args to the daemon. Injected so
// the forwarding path is testable without opening a socket.
type hookDeliverer func(args []string) error

// buildHookRunE returns the RunE for the "hook" command. It reads Claude Code
// hook JSON from stdin and forwards it to the daemon as hook-event args.
func buildHookRunE() func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		return runHook(os.Stdin, cmd.ErrOrStderr(), deliverHookEvent)
	}
}

// deliverHookEvent resolves the daemon address/token and performs the round-trip.
func deliverHookEvent(args []string) error {
	c := connectDaemon()
	if c == nil {
		return errors.WithDetails("no reachable daemon for hook delivery")
	}
	if _, err := c.RunRemote(args); err != nil {
		return err
	}
	return nil
}

// runHook reads a Claude Code hook payload from stdin and forwards the event to
// the daemon via deliver. It tolerates event-key renames across Claude versions
// and never drops a non-empty payload or a failed delivery without a stderr line,
// so container hook failures surface in the agent's output.log (ticket 428).
func runHook(stdin io.Reader, stderr io.Writer, deliver hookDeliverer) error {
	data, err := io.ReadAll(stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "human hook: cannot read stdin: %v\n", err)
		return nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil // genuinely empty invocation — nothing to report
	}

	var input hookInput
	// Best-effort: unknown/renamed top-level keys must not abort parsing.
	_ = json.Unmarshal(data, &input)

	eventName := resolveEventName(data, input.EventName)
	if eventName == "" {
		_, _ = fmt.Fprintf(stderr, // #nosec G705 -- CLI terminal output, not web
			"human hook: no recognizable event-name key in %d-byte payload; dropping (update the known-key set in resolveEventName)\n",
			len(data))
		return nil
	}

	agentName := os.Getenv("HUMAN_AGENT_NAME")
	// The daemon minted this when it launched the run and injected it here; it is
	// how the daemon recognises its own work on the way back (SC-4082). Absent
	// outside a daemon-launched container, which is the "no run id" case the
	// daemon handles explicitly.
	runID := os.Getenv("HUMAN_RUN_ID")
	toolInput := compactJSON(input.ToolInput) // "" when absent
	subagentType, model := resolveAttribution(input)
	args := []string{"hook-event", eventName, input.SessionID, input.Cwd,
		input.NotificationType, input.ToolName, input.ErrorType, agentName,
		toolInput, subagentType, model, runID}
	if err := deliver(args); err != nil {
		_, _ = fmt.Fprintf(stderr, "human hook: failed to deliver %q event to daemon: %v\n", eventName, err) // #nosec G705 -- CLI terminal output, not web
	}
	return nil
}

// compactJSON returns tool input as single-line JSON, or "" when empty/invalid.
// The daemon bounds and redacts it before persisting; here we only normalize.
func compactJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "" // malformed input is dropped rather than forwarded raw
	}
	return buf.String()
}

// resolveEventName recovers the hook event name across Claude Code schema
// versions. It trusts the struct-parsed canonical value first, then scans the
// raw top-level JSON for any known alias key, guarding against a future rename
// silently zeroing out the event.
func resolveEventName(data []byte, structVal string) string {
	if structVal != "" {
		return structVal
	}
	knownKeys := []string{"hook_event_name", "hookEventName", "event_name", "eventName", "event"}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	for _, k := range knownKeys {
		v, ok := raw[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}

// localSubcommands lists commands that must execute locally rather than
// being forwarded to the daemon.
var localSubcommands = map[string]bool{
	"daemon":        true,
	"chrome-bridge": true,
	"commits":       true,
	"pipeline":      true,
	"install":       true,
	"init":          true,
	"usage":         true,
	"index":         true,
	"agent-context": true,
	"doctor":        true,
	"ping":          true,
	"proxy":         true,
	"hook":          true,
	"agent":         true,
	// Describes the caller's own checkout and environment — the one thing the
	// daemon cannot see on its behalf.
	"capabilities": true,
	// Answers from the machine compiled into THIS binary. Forwarding it would
	// make the one caller it exists for — an agent in a container, on a project
	// whose daemon it cannot reach — unable to ask, and would answer from the
	// daemon's build rather than the asker's, so two binaries at different
	// versions would silently disagree about which document they quoted.
	"fsm": true,
	// bug/security create call the daemon's bug-create/security-create route
	// directly (like the desktop board buttons); they must not be forwarded, or
	// the RunE would open a reentrant connection back into the daemon.
	"bug":      true,
	"security": true,
}

// globalValueFlags lists global persistent flags that take a value. When these
// appear in space-separated form (e.g. "--tracker work"), the value token must
// be skipped so it isn't mistaken for the subcommand name. Shared with the
// daemon's destructive-command detection via internal/cliflags so the two
// cannot drift apart.
var globalValueFlags = cliflags.ValueFlags

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
		if a == "codenav" {
			return codenavRunsLocally(args)
		}
		return localSubcommands[a]
	}
	return false
}

// codenavRunsLocally decides whether a `human codenav …` invocation must run
// against the caller's own filesystem instead of the daemon's shared index.
// `index`/`rm` manage the local index and `impact --diff` needs the caller's
// uncommitted working changes, which live only in the caller's worktree; every
// other verb is a pure index query and is forwarded so all callers share one
// daemon-kept index.
func codenavRunsLocally(args []string) bool {
	verb, hasDiff := codenavVerb(args)
	switch verb {
	case "", "index", "rm":
		return true
	case "impact":
		return hasDiff
	default:
		return false
	}
}

// codenavVerb extracts the codenav subcommand (first positional after
// "codenav") and whether --diff is present, skipping value-taking global flags
// so a flag value is never mistaken for the verb.
func codenavVerb(args []string) (verb string, hasDiff bool) {
	seenCodenav := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--diff" {
			hasDiff = true
			continue
		}
		if len(a) > 0 && a[0] == '-' {
			if globalValueFlags[a] && i+1 < len(args) {
				i++
			}
			continue
		}
		if !seenCodenav {
			if a == "codenav" {
				seenCodenav = true
			}
			continue
		}
		if verb == "" {
			verb = a
		}
	}
	return verb, hasDiff
}

// --- update notices ---

// isTTY reports whether stderr is an interactive terminal. Update notices
// and skew warnings are suppressed in non-interactive contexts (pipes, CI,
// daemon child processes) to avoid polluting structured output.
func isTTY() bool {
	return term.IsTerminal(int(os.Stderr.Fd())) // #nosec G115 -- fd is from os.Stderr.Fd(), safe range
}

// printUpdateNotice writes a one-line update hint to stderr when a newer
// version is available in the GitHub releases cache. The check itself runs
// in the background so it never blocks the command on the critical path.
func printUpdateNotice(currentVersion string) {
	if currentVersion == "" || currentVersion == "dev" {
		return
	}
	// Daemon child processes share the same binary but should not print to
	// the operator's terminal — messages would appear in the daemon log.
	if os.Getenv("_HUMAN_DAEMON_CHILD") != "" {
		return
	}
	if !isTTY() {
		return
	}
	cachePath := update.CachePath()
	// Refresh the cache in the background so the notice appears on the next
	// invocation after the cache goes stale, never blocking this one.
	go update.CheckAndRefresh(cachePath)
	latest := update.CachedLatestVersion(cachePath)
	if update.IsNewer(currentVersion, latest) {
		hint := update.InstallHint()
		fmt.Fprintf(os.Stderr, "\nA new version %s is available — run `%s`\n\n", latest, hint)
	}
}

// printDaemonSkewWarning alerts the operator when the CLI binary version
// differs from the version of the currently running daemon. A skew can mean
// the daemon still has the old code path and a restart is needed.
func printDaemonSkewWarning(clientVersion, daemonVersion string) {
	if clientVersion == "" || clientVersion == "dev" {
		return
	}
	if daemonVersion == "" || daemonVersion == "dev" {
		return
	}
	if clientVersion == daemonVersion {
		return
	}
	if !isTTY() {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: client v%s is talking to daemon v%s — consider restarting the daemon after upgrading\n", clientVersion, daemonVersion)
}

// --- main ---

// subcmdFromBinary checks whether the binary was invoked via a symlink
// like "human-browser" and returns the implied subcommand (e.g. "browser").
// Returns "" when os.Args[0] is just "human" or unrecognised.
func subcmdFromBinary() string {
	base := filepath.Base(os.Args[0])
	// Strip common extensions (.exe on Windows).
	base = strings.TrimSuffix(base, ".exe")
	if strings.HasPrefix(base, "human-") {
		return base[len("human-"):]
	}
	return ""
}

// connectDaemon locates the daemon and, when the address was discovered rather
// than named by the caller, propagates the chrome and proxy addresses into the
// process environment. It returns nil when there is no daemon to talk to, which
// is not an error: the command then runs locally.
//
// The protocol refusal is the one failure that must not be swallowed — running
// locally against a daemon that is merely too old is worse than stopping.
func connectDaemon() *daemon.Client {
	c, err := daemon.Connect()
	if err != nil {
		if daemon.IsProtocolError(err) {
			errors.LogError(err).Msg("daemon too old for this client")
			os.Exit(1)
		}
		return nil
	}
	if os.Getenv("HUMAN_DAEMON_ADDR") == "" {
		applyDaemonInfo(c.Info())
	}
	return c
}

// applyDaemonInfo propagates the chrome and proxy addresses into environment
// variables. The chrome bridge and the container proxy redirect read them from
// there, so discovery is not finished until they are set.
func applyDaemonInfo(info daemon.DaemonInfo) {
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
}

func main() {
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	// Stamp the build version into every daemon request so the daemon's
	// version gate can reject a protocol-stale client with a clear error.
	daemon.ClientVersion = version

	// Busybox-style dispatch: "human-browser URL" → "human browser URL".
	args := os.Args[1:]
	if sub := subcmdFromBinary(); sub != "" {
		args = append([]string{sub}, args...)
	}

	// Client mode: forward to the daemon when there is one. The protocol gate is
	// applied inside connectDaemon, so a daemon too old to serve this client is
	// refused before any request rather than after a cryptic unknown-command.
	client := connectDaemon()

	// Warn when the CLI binary and the running daemon are on different versions.
	// Skipped silently when the daemon is unreachable or its info file is absent.
	if client != nil {
		printDaemonSkewWarning(version, client.Info().Version)
	}
	// Passive update notice — fires a background goroutine then reads the cache.
	printUpdateNotice(version)

	// "daemon" subcommands must run locally.
	if client != nil && !isLocalSubcommand(args) {
		exitCode, err := client.RunRemote(args)
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
