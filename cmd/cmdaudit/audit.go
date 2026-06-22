// Package cmdaudit implements the "human audit" command tree, which reads the
// structured trail of mutating tracker actions the daemon records. The daemon
// owns the audit database, so every read is forwarded to it.
package cmdaudit

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/audit"
	"github.com/gethuman-sh/human/internal/daemon"
)

// noDaemonMsg is shown when the daemon is unreachable. Audit is recorded only
// by the daemon, so without it there is nothing to read.
const noDaemonMsg = "audit trail is recorded by the daemon; start it with `human daemon start`"

// BuildAuditCmd creates the "audit" command tree.
func BuildAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "audit",
		Short:   "Inspect the structured trail of AI agent actions against trackers",
		GroupID: "utility",
	}
	cmd.AddCommand(buildAuditListCmd())
	cmd.AddCommand(buildAuditShowCmd())
	return cmd
}

func buildAuditListCmd() *cobra.Command {
	var (
		since    string
		until    string
		subject  string
		tracker  string
		limit    int
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recorded audit events, newest first",
		RunE: func(cmd *cobra.Command, _ []string) error {
			addr, token, err := resolveDaemon(cmd.OutOrStdout())
			if err != nil {
				return nil // resolveDaemon already printed the guidance
			}

			filterArgs := buildFilterArgs(since, until, subject, tracker, limit)
			events, err := daemon.QueryAudit(addr, token, filterArgs)
			if err != nil {
				return errors.WrapWithDetails(err, "failed to query audit trail")
			}

			if asJSON {
				return writeJSON(cmd.OutOrStdout(), events)
			}
			return renderTable(cmd.OutOrStdout(), events)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "earliest event time (RFC3339)")
	cmd.Flags().StringVar(&until, "until", "", "latest event time (RFC3339)")
	cmd.Flags().StringVar(&subject, "subject", "", "filter by ticket key")
	cmd.Flags().StringVar(&tracker, "tracker", "", "filter by tracker kind")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum number of events")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit raw CloudEvents JSON")
	return cmd
}

func buildAuditShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <EVENT_ID>",
		Short: "Show the full CloudEvents envelope for one event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, token, err := resolveDaemon(cmd.OutOrStdout())
			if err != nil {
				return nil
			}

			// A high limit so the target event is in the returned window
			// regardless of how many events precede it.
			events, err := daemon.QueryAudit(addr, token, []string{"--limit", "10000"})
			if err != nil {
				return errors.WrapWithDetails(err, "failed to query audit trail")
			}

			for _, e := range events {
				if e.ID == args[0] {
					return writeJSON(cmd.OutOrStdout(), e)
				}
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "event not found: %s\n", args[0])
			return nil
		},
	}
}

// resolveDaemon reads the daemon addr+token. When the daemon is unreachable it
// prints guidance to out and returns an error so callers can stop early.
func resolveDaemon(out io.Writer) (addr, token string, err error) {
	info, err := daemon.ReadInfo()
	if err != nil || !info.IsReachable() {
		_, _ = fmt.Fprintln(out, noDaemonMsg)
		return "", "", errors.WithDetails("daemon not reachable")
	}
	return info.Addr, info.Token, nil
}

// buildFilterArgs converts the cobra flags into the flat token slice the
// daemon's parseAuditFilter understands. Empty/zero flags are omitted so the
// daemon falls back to its defaults.
func buildFilterArgs(since, until, subject, tracker string, limit int) []string {
	var args []string
	if since != "" {
		args = append(args, "--since", since)
	}
	if until != "" {
		args = append(args, "--until", until)
	}
	if subject != "" {
		args = append(args, "--subject", subject)
	}
	if tracker != "" {
		args = append(args, "--tracker", tracker)
	}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	return args
}

func writeJSON(out io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errors.WrapWithDetails(err, "marshal audit JSON")
	}
	_, _ = fmt.Fprintln(out, string(data))
	return nil
}

// renderTable prints a fixed-width summary of the events.
func renderTable(out io.Writer, events []audit.Event) error {
	if len(events) == 0 {
		_, _ = fmt.Fprintln(out, "no audit events")
		return nil
	}

	_, _ = fmt.Fprintf(out, "%-20s  %-28s  %-26s  %-12s  %-8s  %s\n",
		"TIME", "SOURCE", "TYPE", "SUBJECT", "OUTCOME", "RATIONALE")
	for _, e := range events {
		_, _ = fmt.Fprintf(out, "%-20s  %-28s  %-26s  %-12s  %-8s  %s\n",
			e.Time.Format("2006-01-02 15:04:05"),
			truncate(e.Source, 28),
			truncate(e.Type, 26),
			truncate(e.Subject, 12),
			truncate(string(e.Data.Outcome), 8),
			truncate(e.Data.Decision.Rationale, 60),
		)
	}
	return nil
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
