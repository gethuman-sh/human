// Package cmdstate surfaces the agent state store as CLI commands: the durable
// working memory one pipeline stage leaves for the next, and the stage claims
// that let a fresh agent take over from one that died mid-run.
//
// The command is deliberately absent from main.go's localSubcommands, so it is
// forwarded to the daemon and executes there. That is what makes the store
// shared: every board container, every agent, and the host CLI read and write
// the one database on the daemon host.
package cmdstate

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/env"
)

// storeOpener yields the state store. Injected so command tests run against a
// fake and never touch SQLite or the user's home directory.
type storeOpener func() (agentstate.Store, error)

func defaultOpener() (agentstate.Store, error) {
	return agentstate.Open(agentstate.DefaultDBPath())
}

// BuildStateCmd creates the top-level "state" command.
func BuildStateCmd() *cobra.Command {
	return buildStateCmd(defaultOpener)
}

func buildStateCmd(open storeOpener) *cobra.Command {
	stateCmd := &cobra.Command{
		Use:   "state",
		Short: "Store and hand over an agent's working state for a ticket",
		Long: `Durable working memory for pipeline agents, kept by the daemon.

A stage records what it learned under a ticket scope; the next stage — or a
fresh agent taking over from one that died — reads it back instead of
re-deriving it. State is local to the daemon host and is never posted to a
tracker: the [human:*] marker comments remain the public record.

Reserved names by convention (the namespace stays open):
  stage.<name>              JSON stage report: status, evidence, blockers, next
  decisions                 answers to preflight questions, read by later stages
  budget.<stage>.attempts   retry counter, maintained with "state incr"
  budget.<stage>.flakes     failures classified as flaky, not charged as attempts
  capabilities              what this run is allowed to do (push, PR, deploy)
  <stage>.evidence          the context that would otherwise die at a handoff`,
	}
	stateCmd.AddCommand(
		buildSetCmd(open), buildGetCmd(open), buildListCmd(open),
		buildRmCmd(open), buildIncrCmd(open),
		buildClaimCmd(open), buildReleaseCmd(open), buildClaimsCmd(open),
		buildPruneCmd(open),
	)
	return stateCmd
}

// withStore opens the store, runs fn, and always closes it.
func withStore(open storeOpener, fn func(agentstate.Store) error) error {
	store, err := open()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return fn(store)
}

// metaFromContext reads the writing agent's identity from the context-bound
// env. It must not use os.Getenv: a board container's HUMAN_AGENT_NAME arrives
// on the forwarded request, not in the daemon's own process environment.
func metaFromContext(ctx context.Context, agentOverride string) agentstate.Meta {
	agent := agentOverride
	if agent == "" {
		agent = env.Lookup(ctx, "HUMAN_AGENT_NAME")
	}
	return agentstate.Meta{Agent: agent, RunID: env.Lookup(ctx, "HUMAN_DAEMON_ID")}
}

func buildSetCmd(open storeOpener) *cobra.Command {
	var value, bodyFile, agent string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "set SCOPE NAME",
		Short: "Write a state value for a ticket scope",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := resolveValue(value, bodyFile, cmd.InOrStdin())
			if err != nil {
				return err
			}
			format := agentstate.FormatText
			if asJSON {
				format = agentstate.FormatJSON
			}
			return withStore(open, func(store agentstate.Store) error {
				entry, err := store.Set(cmd.Context(), args[0], args[1], body, format,
					metaFromContext(cmd.Context(), agent))
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s (%d bytes)\n",
					entry.Scope, entry.Name, len(entry.Value))
				return err
			})
		},
	}
	cmd.Flags().StringVar(&value, "value", "", "Value to store")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Read the value from a file, or - for stdin")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Mark the value as JSON (validated on write)")
	// Attribution is what a successor inherits by: state written without an
	// agent name belongs to nobody and is reported as nothing left behind.
	cmd.Flags().StringVar(&agent, "agent", "", "Writing agent (defaults to $HUMAN_AGENT_NAME)")
	return cmd
}

func buildGetCmd(open storeOpener) *cobra.Command {
	var withMeta bool
	var fallback, field string
	var hasFallback bool
	cmd := &cobra.Command{
		Use:   "get SCOPE NAME",
		Short: "Read a state value (exit 1 when absent unless --default is given)",
		Long: `Read a state value.

With --field, read one key out of a JSON value — so a stage report is consumed
as data rather than parsed out of prose:

  human state get SC-1 stage.triage --field exit`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			hasFallback = cmd.Flags().Changed("default")
			return withStore(open, func(store agentstate.Store) error {
				entry, err := store.Get(cmd.Context(), args[0], args[1])
				if stderrors.Is(err, agentstate.ErrNotFound) && hasFallback {
					_, printErr := fmt.Fprintln(cmd.OutOrStdout(), fallback)
					return printErr
				}
				if err != nil {
					return err
				}
				if withMeta {
					return writeJSON(cmd.OutOrStdout(), entry)
				}
				return printValue(cmd.OutOrStdout(), entry, field, fallback, hasFallback)
			})
		},
	}
	cmd.Flags().BoolVar(&withMeta, "meta", false, "Print the full entry as JSON (value plus provenance)")
	cmd.Flags().StringVar(&fallback, "default", "", "Print this instead of failing when the entry (or --field) is absent")
	cmd.Flags().StringVar(&field, "field", "", "Print one top-level key of a JSON value")
	return cmd
}

// printValue writes the entry's value, or one field of it when asked.
func printValue(out io.Writer, entry agentstate.Entry, field, fallback string, hasFallback bool) error {
	if field == "" {
		_, err := fmt.Fprintln(out, entry.Value)
		return err
	}
	value, found, err := jsonField(entry.Value, field)
	if err != nil {
		return err
	}
	if !found {
		if hasFallback {
			_, printErr := fmt.Fprintln(out, fallback)
			return printErr
		}
		return errors.WithDetails("no such field in the stored JSON value",
			"scope", entry.Scope, "name", entry.Name, "field", field)
	}
	_, err = fmt.Fprintln(out, value)
	return err
}

// jsonField extracts one top-level key. A string is printed bare so it can be
// compared directly in a shell test; anything else is re-encoded as JSON.
func jsonField(value, field string) (string, bool, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(value), &obj); err != nil {
		return "", false, errors.WrapWithDetails(err, "value is not a JSON object", "field", field)
	}
	raw, ok := obj[field]
	if !ok {
		return "", false, nil
	}
	if s, isString := raw.(string); isString {
		return s, true, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return "", false, errors.WrapWithDetails(err, "re-encoding field", "field", field)
	}
	return string(encoded), true, nil
}

func buildListCmd(open storeOpener) *cobra.Command {
	var prefix string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list SCOPE",
		Short: "List the state entries of a scope",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(open, func(store agentstate.Store) error {
				entries, err := store.List(cmd.Context(), args[0], prefix)
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), entries)
				}
				return writeEntryTable(cmd.OutOrStdout(), entries)
			})
		},
	}
	cmd.Flags().StringVar(&prefix, "prefix", "", "Only names starting with this prefix")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of a table")
	return cmd
}

func buildRmCmd(open storeOpener) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "rm SCOPE [NAME]",
		Short: "Remove one entry, or every entry of a scope with --all",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 2) {
				return errors.WithDetails("give either a NAME or --all, not both or neither")
			}
			return withStore(open, func(store agentstate.Store) error {
				if all {
					n, err := store.DeleteScope(cmd.Context(), args[0])
					if err != nil {
						return err
					}
					_, printErr := fmt.Fprintf(cmd.OutOrStdout(), "removed %d entries\n", n)
					return printErr
				}
				removed, err := store.Delete(cmd.Context(), args[0], args[1])
				if err != nil {
					return err
				}
				_, printErr := fmt.Fprintln(cmd.OutOrStdout(), removedMessage(removed))
				return printErr
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Remove every entry and claim of the scope")
	return cmd
}

func removedMessage(removed bool) string {
	if removed {
		return "removed"
	}
	return "no such entry"
}

func buildIncrCmd(open storeOpener) *cobra.Command {
	var by int64
	var agent string
	cmd := &cobra.Command{
		Use:   "incr SCOPE NAME",
		Short: "Add to a counter entry and print the new total",
		Long: `Add to a counter entry and print the new total.

Counters back the retry budgets: a stage charges an attempt only after it has
established that a failure is real, so a flaky test never consumes the run.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(open, func(store agentstate.Store) error {
				n, err := store.Incr(cmd.Context(), args[0], args[1], by, metaFromContext(cmd.Context(), agent))
				if err != nil {
					return err
				}
				_, printErr := fmt.Fprintln(cmd.OutOrStdout(), n)
				return printErr
			})
		},
	}
	cmd.Flags().Int64Var(&by, "by", 1, "Amount to add (may be negative)")
	cmd.Flags().StringVar(&agent, "agent", "", "Writing agent (defaults to $HUMAN_AGENT_NAME)")
	return cmd
}

func buildClaimCmd(open storeOpener) *cobra.Command {
	var stage, agent string
	var ttl time.Duration
	var takeover, asJSON bool
	cmd := &cobra.Command{
		Use:   "claim SCOPE",
		Short: "Claim a stage of a scope, taking over an abandoned claim",
		Long: `Claim a stage of a scope.

A claim held by another agent that is still heartbeating is refused, naming the
holder. Once its heartbeat lapses past the TTL the stage is considered
abandoned and the claim is granted: the result then names the displaced agent
and the state keys it left behind, so its work is inherited rather than redone.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(open, func(store agentstate.Store) error {
				res, err := store.Claim(cmd.Context(), agentstate.ClaimRequest{
					Scope:    args[0],
					Stage:    stage,
					Meta:     metaFromContext(cmd.Context(), agent),
					TTL:      ttl,
					Takeover: takeover,
				})
				if err != nil {
					return err
				}
				return reportClaim(cmd.OutOrStdout(), res, asJSON)
			})
		},
	}
	cmd.Flags().StringVar(&stage, "stage", "", "Stage to claim (triage, plan, fix, verify, review, deploy, …)")
	cmd.Flags().StringVar(&agent, "agent", "", "Claiming agent (defaults to $HUMAN_AGENT_NAME)")
	cmd.Flags().DurationVar(&ttl, "ttl", agentstate.DefaultClaimTTL, "How long the claim stays live without a heartbeat")
	cmd.Flags().BoolVar(&takeover, "takeover", false, "Displace even a live claim")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the claim result as JSON")
	_ = cmd.MarkFlagRequired("stage")
	return cmd
}

// reportClaim prints the outcome and fails the command when the claim was
// refused, so a caller can branch on the exit code alone.
func reportClaim(out io.Writer, res agentstate.ClaimResult, asJSON bool) error {
	if asJSON {
		if err := writeJSON(out, res); err != nil {
			return err
		}
	} else if err := writeClaimText(out, res); err != nil {
		return err
	}
	if !res.Granted {
		return errors.WrapWithDetails(agentstate.ErrClaimHeld, "claim refused",
			"scope", res.Claim.Scope, "stage", res.Claim.Stage, "holder", res.Claim.Agent)
	}
	return nil
}

func writeClaimText(out io.Writer, res agentstate.ClaimResult) error {
	if !res.Granted {
		_, err := fmt.Fprintf(out, "refused: %s/%s is held by %s (last heartbeat %s)\n",
			res.Claim.Scope, res.Claim.Stage, res.Claim.Agent,
			res.Claim.HeartbeatAt.Format(time.RFC3339))
		return err
	}
	if _, err := fmt.Fprintf(out, "claimed %s/%s as %s\n",
		res.Claim.Scope, res.Claim.Stage, res.Claim.Agent); err != nil {
		return err
	}
	if res.Displaced == nil {
		return nil
	}
	if _, err := fmt.Fprintf(out, "took over from %s (last heartbeat %s)\n",
		res.Displaced.Agent, res.Displaced.HeartbeatAt.Format(time.RFC3339)); err != nil {
		return err
	}
	if len(res.InheritedKeys) == 0 {
		_, err := fmt.Fprintln(out, "it left no state behind")
		return err
	}
	_, err := fmt.Fprintf(out, "inherited state: %s\n", strings.Join(res.InheritedKeys, ", "))
	return err
}

func buildReleaseCmd(open storeOpener) *cobra.Command {
	var stage, agent string
	cmd := &cobra.Command{
		Use:   "release SCOPE",
		Short: "Hand a stage claim back",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(open, func(store agentstate.Store) error {
				released, err := store.Release(cmd.Context(), args[0], stage,
					metaFromContext(cmd.Context(), agent).Agent)
				if err != nil {
					return err
				}
				_, printErr := fmt.Fprintln(cmd.OutOrStdout(), releasedMessage(released))
				return printErr
			})
		},
	}
	cmd.Flags().StringVar(&stage, "stage", "", "Stage to release")
	cmd.Flags().StringVar(&agent, "agent", "", "Releasing agent (defaults to $HUMAN_AGENT_NAME)")
	_ = cmd.MarkFlagRequired("stage")
	return cmd
}

func releasedMessage(released bool) string {
	if released {
		return "released"
	}
	return "no live claim to release"
}

func buildClaimsCmd(open storeOpener) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "claims SCOPE",
		Short: "Show who holds which stage of a scope",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(open, func(store agentstate.Store) error {
				claims, err := store.Claims(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), claims)
				}
				return writeClaimTable(cmd.OutOrStdout(), claims)
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of a table")
	return cmd
}

func buildPruneCmd(open storeOpener) *cobra.Command {
	var olderThan time.Duration
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Drop state not updated within the retention window",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withStore(open, func(store agentstate.Store) error {
				n, err := store.Prune(cmd.Context(), time.Now().UTC().Add(-olderThan))
				if err != nil {
					return err
				}
				_, printErr := fmt.Fprintf(cmd.OutOrStdout(), "pruned %d entries\n", n)
				return printErr
			})
		},
	}
	cmd.Flags().DurationVar(&olderThan, "older-than", agentstate.DefaultRetention, "Retention window")
	return cmd
}

func writeEntryTable(out io.Writer, entries []agentstate.Entry) error {
	if len(entries) == 0 {
		_, err := fmt.Fprintln(out, "no state")
		return err
	}
	for _, e := range entries {
		if _, err := fmt.Fprintf(out, "%-32s %-6s %6d  %s  %s\n",
			e.Name, e.Format, len(e.Value), e.UpdatedAt.Format(time.RFC3339), e.Agent); err != nil {
			return err
		}
	}
	return nil
}

func writeClaimTable(out io.Writer, claims []agentstate.Claim) error {
	if len(claims) == 0 {
		_, err := fmt.Fprintln(out, "no claims")
		return err
	}
	for _, c := range claims {
		state := "held"
		if c.ReleasedAt != nil {
			state = "released"
		}
		if _, err := fmt.Fprintf(out, "%-12s %-8s %-24s %s\n",
			c.Stage, state, c.Agent, c.HeartbeatAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// resolveValue picks the value from --value, --body-file, or stdin ("-").
func resolveValue(value, bodyFile string, stdin io.Reader) (string, error) {
	if value != "" && bodyFile != "" {
		return "", errors.WithDetails("use either --value or --body-file, not both")
	}
	if bodyFile == "" {
		return value, nil
	}
	if bodyFile == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", errors.WrapWithDetails(err, "reading value from stdin")
		}
		return strings.TrimRight(string(data), "\n"), nil
	}
	data, err := os.ReadFile(bodyFile) // #nosec G304 -- path is an explicit user-supplied CLI argument
	if err != nil {
		return "", errors.WrapWithDetails(err, "reading value file", "path", bodyFile)
	}
	return strings.TrimRight(string(data), "\n"), nil
}
