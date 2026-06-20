package dispatch

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/messaging/telegram"
)

type stubFetcher struct {
	updates []telegram.Update
	err     error
	acked   []int
	ackErr  error
}

func (f *stubFetcher) GetUpdates(_ context.Context, _ int) ([]telegram.Update, error) {
	return f.updates, f.err
}

func (f *stubFetcher) AckUpdate(_ context.Context, updateID int) error {
	if f.ackErr != nil {
		return f.ackErr
	}
	f.acked = append(f.acked, updateID)
	return nil
}

func TestTelegramSource_FetchMessages(t *testing.T) {
	fetcher := &stubFetcher{
		updates: []telegram.Update{
			{
				UpdateID: 100,
				Message: &telegram.Message{
					MessageID: 1,
					From:      &telegram.User{ID: 42, FirstName: "John", LastName: "Doe"},
					Chat:      telegram.Chat{ID: 42, Type: "private"},
					Text:      "fix the bug",
				},
			},
			{
				UpdateID: 101,
				Message: &telegram.Message{
					MessageID: 2,
					From:      &telegram.User{ID: 43, FirstName: "Jane"},
					Chat:      telegram.Chat{ID: 43, Type: "private"},
					Text:      "add feature",
				},
			},
		},
	}

	// No AllowedUsers configured → default-deny, all messages filtered.
	source := &TelegramSource{Client: fetcher}
	messages, err := source.FetchMessages(context.Background())
	require.NoError(t, err)
	require.Empty(t, messages)

	// Rejected updates must be acked so they do not linger in the Telegram
	// pending queue, and the rejection counter must reflect them.
	assert.Equal(t, []int{100, 101}, fetcher.acked)
	assert.Equal(t, uint64(2), source.RejectedCount())
}

func TestTelegramSource_FetchMessages_WithAllowedUsers(t *testing.T) {
	fetcher := &stubFetcher{
		updates: []telegram.Update{
			{
				UpdateID: 100,
				Message: &telegram.Message{
					MessageID: 1,
					From:      &telegram.User{ID: 42, FirstName: "John", LastName: "Doe"},
					Chat:      telegram.Chat{ID: 42, Type: "private"},
					Text:      "fix the bug",
				},
			},
			{
				UpdateID: 101,
				Message: &telegram.Message{
					MessageID: 2,
					From:      &telegram.User{ID: 43, FirstName: "Jane"},
					Chat:      telegram.Chat{ID: 43, Type: "private"},
					Text:      "add feature",
				},
			},
		},
	}

	source := &TelegramSource{Client: fetcher, AllowedUsers: []int64{42, 43}}
	messages, err := source.FetchMessages(context.Background())
	require.NoError(t, err)
	require.Len(t, messages, 2)

	assert.Equal(t, 100, messages[0].UpdateID)
	assert.Equal(t, int64(42), messages[0].ChatID)
	assert.Equal(t, "John Doe", messages[0].From)
	assert.Equal(t, "fix the bug", messages[0].Text)

	assert.Equal(t, 101, messages[1].UpdateID)
	assert.Equal(t, "Jane", messages[1].From)
}

func TestTelegramSource_FetchMessages_FilteredByAllowedUsers(t *testing.T) {
	fetcher := &stubFetcher{
		updates: []telegram.Update{
			{UpdateID: 100, Message: &telegram.Message{From: &telegram.User{ID: 42, FirstName: "John"}, Chat: telegram.Chat{ID: 42, Type: "private"}, Text: "allowed"}},
			{UpdateID: 101, Message: &telegram.Message{From: &telegram.User{ID: 99, FirstName: "Eve"}, Chat: telegram.Chat{ID: 99, Type: "private"}, Text: "blocked"}},
		},
	}

	source := &TelegramSource{Client: fetcher, AllowedUsers: []int64{42}}
	messages, err := source.FetchMessages(context.Background())
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "allowed", messages[0].Text)

	// The allowed update (100) must NOT be pre-acked here — the dispatcher
	// acks after successful dispatch. Only the rejected update (101) is.
	assert.Equal(t, []int{101}, fetcher.acked)
	assert.Equal(t, uint64(1), source.RejectedCount())
}

func TestTelegramSource_FetchMessages_NilMessage(t *testing.T) {
	fetcher := &stubFetcher{
		updates: []telegram.Update{
			{UpdateID: 100, Message: nil},
		},
	}

	source := &TelegramSource{Client: fetcher}
	messages, err := source.FetchMessages(context.Background())
	require.NoError(t, err)
	assert.Empty(t, messages)

	// Malformed updates (nil Message) are skipped but NOT acked and NOT
	// counted as rejections — they may represent a future Telegram update
	// shape we should not silently consume.
	assert.Empty(t, fetcher.acked)
	assert.Equal(t, uint64(0), source.RejectedCount())
}

func TestTelegramSource_FetchMessages_Error(t *testing.T) {
	fetcher := &stubFetcher{err: fmt.Errorf("network error")}
	source := &TelegramSource{Client: fetcher}
	_, err := source.FetchMessages(context.Background())
	require.Error(t, err)
}

// A message from an allowlisted user in a group chat is rejected by default —
// group-chat dispatch is a distinct trust surface from private DMs and must
// be opted in per chat via AllowedChats.
func TestTelegramSource_FetchMessages_GroupChatDeniedByDefault(t *testing.T) {
	fetcher := &stubFetcher{
		updates: []telegram.Update{
			{UpdateID: 200, Message: &telegram.Message{From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: -1001, Type: "group"}, Text: "do a thing"}},
		},
	}
	source := &TelegramSource{Client: fetcher, AllowedUsers: []int64{42}}
	messages, err := source.FetchMessages(context.Background())
	require.NoError(t, err)
	require.Empty(t, messages)
	assert.Equal(t, []int{200}, fetcher.acked)
	assert.Equal(t, uint64(1), source.RejectedCount())
}

// The same user in the same group chat is accepted once that chat is listed
// in AllowedChats.
func TestTelegramSource_FetchMessages_GroupChatAllowedWhenOptedIn(t *testing.T) {
	fetcher := &stubFetcher{
		updates: []telegram.Update{
			{UpdateID: 200, Message: &telegram.Message{From: &telegram.User{ID: 42, FirstName: "Alice"}, Chat: telegram.Chat{ID: -1001, Type: "supergroup"}, Text: "do a thing"}},
		},
	}
	source := &TelegramSource{
		Client:       fetcher,
		AllowedUsers: []int64{42},
		AllowedChats: []int64{-1001},
	}
	messages, err := source.FetchMessages(context.Background())
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "do a thing", messages[0].Text)
	assert.Empty(t, fetcher.acked) // allowed → not acked here; dispatcher acks after send
	assert.Equal(t, uint64(0), source.RejectedCount())
}

// Listing a chat in AllowedChats does not create a back-door for
// non-allowlisted users in that chat.
func TestTelegramSource_FetchMessages_GroupChatAllowlistDoesNotBypassUser(t *testing.T) {
	fetcher := &stubFetcher{
		updates: []telegram.Update{
			{UpdateID: 200, Message: &telegram.Message{From: &telegram.User{ID: 99}, Chat: telegram.Chat{ID: -1001, Type: "group"}, Text: "eve here"}},
		},
	}
	source := &TelegramSource{
		Client:       fetcher,
		AllowedUsers: []int64{42},
		AllowedChats: []int64{-1001},
	}
	messages, err := source.FetchMessages(context.Background())
	require.NoError(t, err)
	require.Empty(t, messages)
	assert.Equal(t, []int{200}, fetcher.acked)
	assert.Equal(t, uint64(1), source.RejectedCount())
}

// A failing ack on a rejected update must not abort the batch — the counter
// still increments and the failure is logged but swallowed so subsequent
// updates still get processed.
func TestTelegramSource_FetchMessages_AckFailureOnReject(t *testing.T) {
	fetcher := &stubFetcher{
		updates: []telegram.Update{
			{UpdateID: 100, Message: &telegram.Message{From: &telegram.User{ID: 99}, Chat: telegram.Chat{ID: 99, Type: "private"}, Text: "eve"}},
			{UpdateID: 101, Message: &telegram.Message{From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: 42, Type: "private"}, Text: "alice"}},
		},
		ackErr: fmt.Errorf("telegram unavailable"),
	}
	source := &TelegramSource{Client: fetcher, AllowedUsers: []int64{42}}
	messages, err := source.FetchMessages(context.Background())
	require.NoError(t, err)

	// Allowed message still comes through.
	require.Len(t, messages, 1)
	assert.Equal(t, "alice", messages[0].Text)

	// Rejection counter still increments even though the ack call failed.
	assert.Equal(t, uint64(1), source.RejectedCount())
}

func TestTelegramSource_AckMessage(t *testing.T) {
	fetcher := &stubFetcher{}
	source := &TelegramSource{Client: fetcher}
	err := source.AckMessage(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, []int{100}, fetcher.acked)
}

func TestFormatFrom(t *testing.T) {
	assert.Equal(t, "John Doe", formatFrom(&telegram.User{FirstName: "John", LastName: "Doe"}))
	assert.Equal(t, "Jane", formatFrom(&telegram.User{FirstName: "Jane"}))
	assert.Equal(t, "", formatFrom(nil))
}
