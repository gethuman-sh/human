package cmdslack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/messaging/slack"
)

// slackMessageSender sends a message to Slack.
type slackMessageSender interface {
	SendMessage(ctx context.Context, text string) error
}

// slackMessageLister lists recent messages from a Slack channel.
type slackMessageLister interface {
	ListMessages(ctx context.Context, limit int) ([]slack.MessageSummary, error)
}

// BuildSlackCommands returns the "slack" command tree with send and list subcommands.
func BuildSlackCommands() *cobra.Command {
	slackCmd := &cobra.Command{
		Use:   "slack",
		Short: "Slack notification tools",
	}

	slackCmd.PersistentFlags().String("slack", "", "Named Slack instance from .humanconfig")

	// --- send ---
	sendCmd := &cobra.Command{
		Use:   "send MESSAGE",
		Short: "Send a message to the configured Slack channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, err := resolveSlackInstance(cmd)
			if err != nil {
				return err
			}
			return runSlackSend(cmd.Context(), inst.Client, cmd.OutOrStdout(), args[0])
		},
	}
	slackCmd.AddCommand(sendCmd)

	// --- list ---
	var listTable bool
	var listLimit int
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List recent messages from the configured Slack channel",
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := resolveSlackInstance(cmd)
			if err != nil {
				return err
			}
			return runSlackList(cmd.Context(), inst.Client, cmd.OutOrStdout(), listLimit, listTable)
		},
	}
	listCmd.Flags().BoolVar(&listTable, "table", false, "Output as human-readable table instead of JSON")
	listCmd.Flags().IntVar(&listLimit, "limit", 20, "Maximum number of messages to fetch")
	slackCmd.AddCommand(listCmd)

	return slackCmd
}

func resolveSlackInstance(cmd *cobra.Command) (*slack.Instance, error) {
	name, _ := cmd.Flags().GetString("slack")

	instances, err := slack.LoadInstances(".")
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, errors.WithDetails("no Slack instances configured, add slacks: to .humanconfig.yaml")
	}

	if name != "" {
		for i := range instances {
			if instances[i].Name == name {
				return &instances[i], nil
			}
		}
		return nil, errors.WithDetails("Slack instance not found", "name", name)
	}

	return &instances[0], nil
}

// --- Business logic functions ---

func runSlackSend(ctx context.Context, client slackMessageSender, out io.Writer, message string) error {
	if err := client.SendMessage(ctx, message); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Message sent")
	return nil
}

func runSlackList(ctx context.Context, client slackMessageLister, out io.Writer, limit int, table bool) error {
	msgs, err := client.ListMessages(ctx, limit)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		_, _ = fmt.Fprintln(out, "No messages")
		return nil
	}
	if table {
		return printSlackListTable(out, msgs)
	}
	return printSlackListJSON(out, msgs)
}

// --- Output formatters ---

func printSlackListJSON(w io.Writer, msgs []slack.MessageSummary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(msgs)
}

func printSlackListTable(out io.Writer, msgs []slack.MessageSummary) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "USER\tTS\tTEXT")
	for _, m := range msgs {
		text := m.Text
		if len(text) > 60 {
			text = text[:57] + "..."
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", m.User, m.TS, text)
	}
	return w.Flush()
}
