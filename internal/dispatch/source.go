package dispatch

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/messaging/telegram"
)

// TelegramFetcher is the subset of telegram.Client needed to fetch messages.
type TelegramFetcher interface {
	GetUpdates(ctx context.Context, limit int) ([]telegram.Update, error)
	AckUpdate(ctx context.Context, updateID int) error
}

// TelegramSource adapts a Telegram client to the MessageSource interface.
type TelegramSource struct {
	Client       TelegramFetcher
	AllowedUsers []int64
	AllowedChats []int64 // see telegram.Config.AllowedChats
	Logger       zerolog.Logger

	rejectedCount uint64 // atomic
}

// FetchMessages returns pending Telegram messages filtered by allowed
// (user, chat) pairs. Disallowed updates are acknowledged (via AckUpdate) so
// they do not remain in the Telegram pending queue, logged at WARN with
// structured fields, and counted in RejectedCount() for observability.
func (s *TelegramSource) FetchMessages(ctx context.Context) ([]QueuedMessage, error) {
	updates, err := s.Client.GetUpdates(ctx, 100)
	if err != nil {
		return nil, err
	}

	var messages []QueuedMessage
	for _, u := range updates {
		if u.Message == nil {
			// Malformed update — neither accept nor reject-ack; skip and let
			// it age out naturally. Acking would risk discarding a real
			// message if the shape changes upstream.
			continue
		}
		ok, reason := telegram.IsAllowed(u.Message.From, u.Message.Chat, s.AllowedUsers, s.AllowedChats)
		if !ok {
			s.rejectUpdate(ctx, u, reason)
			continue
		}
		messages = append(messages, QueuedMessage{
			UpdateID: u.UpdateID,
			ChatID:   u.Message.Chat.ID,
			From:     formatFrom(u.Message.From),
			Text:     u.Message.Text,
		})
	}
	return messages, nil
}

// AckMessage acknowledges a Telegram update.
func (s *TelegramSource) AckMessage(ctx context.Context, updateID int) error {
	return s.Client.AckUpdate(ctx, updateID)
}

// RejectedCount returns the total number of updates that have been rejected
// (and acked) across the lifetime of this source.
func (s *TelegramSource) RejectedCount() uint64 {
	return atomic.LoadUint64(&s.rejectedCount)
}

// rejectUpdate acks a disallowed update, logs it at WARN with structured
// fields (but not the message body), and bumps the rejection counter.
func (s *TelegramSource) rejectUpdate(ctx context.Context, u telegram.Update, reason telegram.AuthzReason) {
	atomic.AddUint64(&s.rejectedCount, 1)

	var userID int64
	if u.Message.From != nil {
		userID = u.Message.From.ID
	}
	s.Logger.Warn().
		Int("updateID", u.UpdateID).
		Int64("userID", userID).
		Int64("chatID", u.Message.Chat.ID).
		Str("chatType", u.Message.Chat.Type).
		Int("textLen", len(u.Message.Text)).
		Str("reason", string(reason)).
		Msg("rejected Telegram update")

	if err := s.Client.AckUpdate(ctx, u.UpdateID); err != nil {
		s.Logger.Warn().
			Err(err).
			Int("updateID", u.UpdateID).
			Msg("failed to ack rejected Telegram update")
	}
}

func formatFrom(user *telegram.User) string {
	if user == nil {
		return ""
	}
	name := user.FirstName
	if user.LastName != "" {
		name = fmt.Sprintf("%s %s", name, user.LastName)
	}
	return name
}
