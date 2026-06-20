package cmdtelegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/messaging/telegram"
)

// telegramMessageLister lists pending Telegram messages.
type telegramMessageLister interface {
	GetUpdates(ctx context.Context, limit int) ([]telegram.Update, error)
}

// telegramMessageGetter gets a specific Telegram message by update ID.
type telegramMessageGetter interface {
	GetUpdate(ctx context.Context, updateID int) (*telegram.Update, error)
}

// telegramAcker acknowledges updates up to a given update ID.
type telegramAcker interface {
	AckUpdate(ctx context.Context, updateID int) error
}

func BuildTelegramCommands() *cobra.Command {
	telegramCmd := &cobra.Command{
		Use:   "telegram",
		Short: "Telegram bot message tools",
	}

	telegramCmd.PersistentFlags().String("telegram", "", "Named Telegram instance from .humanconfig")

	// --- list ---
	var listTable bool
	var listLimit int
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List pending messages",
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := resolveTelegramInstance(cmd)
			if err != nil {
				return err
			}
			return runTelegramList(cmd.Context(), inst.Client, cmd.OutOrStdout(), listLimit, listTable, inst.AllowedUsers, inst.AllowedChats)
		},
	}
	listCmd.Flags().BoolVar(&listTable, "table", false, "Output as human-readable table instead of JSON")
	listCmd.Flags().IntVar(&listLimit, "limit", 100, "Maximum number of updates to fetch (1-100)")
	telegramCmd.AddCommand(listCmd)

	// --- get ---
	var getTable bool
	getCmd := &cobra.Command{
		Use:   "get UPDATE_ID",
		Short: "Get a specific message by update ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, err := resolveTelegramInstance(cmd)
			if err != nil {
				return err
			}
			updateID, err := strconv.Atoi(args[0])
			if err != nil {
				return errors.WithDetails("UPDATE_ID must be an integer", "value", args[0])
			}
			return runTelegramGet(cmd.Context(), inst.Client, cmd.OutOrStdout(), updateID, getTable, inst.AllowedUsers, inst.AllowedChats)
		},
	}
	getCmd.Flags().BoolVar(&getTable, "table", false, "Output as human-readable table instead of JSON")
	telegramCmd.AddCommand(getCmd)

	// --- ack ---
	ackCmd := &cobra.Command{
		Use:   "ack UPDATE_ID",
		Short: "Mark messages up to UPDATE_ID as read",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, err := resolveTelegramInstance(cmd)
			if err != nil {
				return err
			}
			updateID, err := strconv.Atoi(args[0])
			if err != nil {
				return errors.WithDetails("UPDATE_ID must be an integer", "value", args[0])
			}
			return runTelegramAck(cmd.Context(), inst.Client, cmd.OutOrStdout(), updateID)
		},
	}
	telegramCmd.AddCommand(ackCmd)

	return telegramCmd
}

func resolveTelegramInstance(cmd *cobra.Command) (*telegram.Instance, error) {
	name, _ := cmd.Flags().GetString("telegram")

	instances, err := telegram.LoadInstances(".")
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, errors.WithDetails("no Telegram instances configured, add telegrams: to .humanconfig.yaml")
	}

	if name != "" {
		for i := range instances {
			if instances[i].Name == name {
				return &instances[i], nil
			}
		}
		return nil, errors.WithDetails("Telegram instance not found", "name", name)
	}

	return &instances[0], nil
}

// --- Business logic functions ---

func runTelegramList(ctx context.Context, client telegramMessageLister, out io.Writer, limit int, table bool, allowedUsers, allowedChats []int64) error {
	updates, err := client.GetUpdates(ctx, limit)
	if err != nil {
		return err
	}
	updates = filterUpdates(updates, allowedUsers, allowedChats)
	summaries := toMessageSummaries(updates)
	if len(summaries) == 0 {
		_, _ = fmt.Fprintln(out, "No pending messages")
		return nil
	}
	if table {
		return printTelegramListTable(out, summaries)
	}
	return printTelegramListJSON(out, summaries)
}

func runTelegramGet(ctx context.Context, client telegramMessageGetter, out io.Writer, updateID int, table bool, allowedUsers, allowedChats []int64) error {
	update, err := client.GetUpdate(ctx, updateID)
	if err != nil {
		return err
	}
	if update == nil {
		return errors.WithDetails("update not found", "updateID", updateID)
	}
	if !isAllowedUpdate(update, allowedUsers, allowedChats) {
		return errors.WithDetails("update not from an allowed (user, chat) pair", "updateID", updateID)
	}
	detail := toMessageDetail(update)
	if table {
		return printTelegramGetTable(out, detail)
	}
	return printTelegramGetJSON(out, detail)
}

func runTelegramAck(ctx context.Context, client telegramAcker, out io.Writer, updateID int) error {
	if err := client.AckUpdate(ctx, updateID); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Acknowledged updates up to %d\n", updateID)
	return nil
}

// --- Filtering ---

// filterUpdates keeps only updates from allowed (user, chat) pairs.
// Uses the same telegram.IsAllowed rule as the dispatcher — in particular,
// an empty allowedUsers list is default-deny (no more CLI/dispatcher
// asymmetry), and group chats require explicit opt-in via allowedChats.
func filterUpdates(updates []telegram.Update, allowedUsers, allowedChats []int64) []telegram.Update {
	var filtered []telegram.Update
	for _, u := range updates {
		if isAllowedUpdate(&u, allowedUsers, allowedChats) {
			filtered = append(filtered, u)
		}
	}
	return filtered
}

func isAllowedUpdate(u *telegram.Update, allowedUsers, allowedChats []int64) bool {
	if u.Message == nil {
		return false
	}
	ok, _ := telegram.IsAllowed(u.Message.From, u.Message.Chat, allowedUsers, allowedChats)
	return ok
}

// --- Conversion helpers ---

func toMessageSummaries(updates []telegram.Update) []telegram.MessageSummary {
	var summaries []telegram.MessageSummary
	for _, u := range updates {
		if u.Message == nil {
			continue
		}
		summaries = append(summaries, telegram.MessageSummary{
			UpdateID:  u.UpdateID,
			MessageID: u.Message.MessageID,
			From:      formatFrom(u.Message.From),
			Date:      time.Unix(u.Message.Date, 0).UTC().Format(time.RFC3339),
			Text:      u.Message.Text,
		})
	}
	return summaries
}

func toMessageDetail(u *telegram.Update) *telegram.MessageDetail {
	if u.Message == nil {
		return &telegram.MessageDetail{UpdateID: u.UpdateID}
	}
	detail := &telegram.MessageDetail{
		UpdateID:  u.UpdateID,
		MessageID: u.Message.MessageID,
		ChatID:    u.Message.Chat.ID,
		ChatType:  u.Message.Chat.Type,
		Date:      time.Unix(u.Message.Date, 0).UTC().Format(time.RFC3339),
		Text:      u.Message.Text,
	}
	if u.Message.From != nil {
		detail.From = formatFrom(u.Message.From)
		detail.FromID = u.Message.From.ID
		detail.Username = u.Message.From.Username
	}
	return detail
}

func formatFrom(user *telegram.User) string {
	if user == nil {
		return ""
	}
	name := user.FirstName
	if user.LastName != "" {
		name += " " + user.LastName
	}
	return name
}

// --- Output formatters ---

func printTelegramListJSON(w io.Writer, summaries []telegram.MessageSummary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(summaries)
}

func printTelegramListTable(out io.Writer, summaries []telegram.MessageSummary) error {
	if len(summaries) == 0 {
		_, _ = fmt.Fprintln(out, "No pending messages")
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "UPDATE ID\tFROM\tDATE\tTEXT")
	for _, s := range summaries {
		text := s.Text
		if len(text) > 60 {
			text = text[:57] + "..."
		}
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", s.UpdateID, s.From, s.Date, text)
	}
	return w.Flush()
}

func printTelegramGetJSON(w io.Writer, detail *telegram.MessageDetail) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(detail)
}

func printTelegramGetTable(out io.Writer, detail *telegram.MessageDetail) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "Update ID:\t%d\n", detail.UpdateID)
	_, _ = fmt.Fprintf(w, "Message ID:\t%d\n", detail.MessageID)
	_, _ = fmt.Fprintf(w, "From:\t%s\n", detail.From)
	_, _ = fmt.Fprintf(w, "From ID:\t%d\n", detail.FromID)
	_, _ = fmt.Fprintf(w, "Username:\t%s\n", detail.Username)
	_, _ = fmt.Fprintf(w, "Chat ID:\t%d\n", detail.ChatID)
	_, _ = fmt.Fprintf(w, "Chat Type:\t%s\n", detail.ChatType)
	_, _ = fmt.Fprintf(w, "Date:\t%s\n", detail.Date)
	_, _ = fmt.Fprintf(w, "Text:\t%s\n", detail.Text)
	return w.Flush()
}
