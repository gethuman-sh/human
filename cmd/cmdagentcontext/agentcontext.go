// Package cmdagentcontext provides the "agent-context" command: a static,
// local, instant block of guidance that primes a coding agent (Claude Code) to
// prefer human's in-session tools (codenav, trackers, knowledge, PRs) over
// ad-hoc approaches. It is wired as a Claude Code SessionStart hook by
// `human install`, and is deliberately free of any config/daemon/vault access
// so it never slows session startup.
package cmdagentcontext

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed agent-context.md
var agentContext string

// BuildAgentContextCmd creates the "agent-context" command.
func BuildAgentContextCmd() *cobra.Command {
	var asHook bool
	cmd := &cobra.Command{
		Use:   "agent-context",
		Short: "Print guidance that primes a coding agent to use human's tools",
		Long: "Prints a static, curated block describing the human capabilities a " +
			"coding agent should use in-session (code navigation, issue tracking, " +
			"context retrieval, PRs). Installed as a Claude Code SessionStart hook by " +
			"`human install`. Local and instant — it loads no config and never " +
			"contacts the daemon.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := strings.TrimRight(agentContext, "\n")
			if asHook {
				// Emit the Claude Code SessionStart hook output that injects the
				// block as additional session context.
				out := map[string]any{
					"hookSpecificOutput": map[string]any{
						"hookEventName":     "SessionStart",
						"additionalContext": body,
					},
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				return enc.Encode(out)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), body)
			return nil
		},
	}
	// --hook emits the SessionStart hook JSON (additionalContext) instead of
	// plain text; the SessionStart hook registered by `human install` uses it.
	cmd.Flags().BoolVar(&asHook, "hook", false, "emit Claude Code SessionStart hook JSON")
	_ = cmd.Flags().MarkHidden("hook")
	return cmd
}
