package cmdtelegram

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/messaging/telegram"
)

// --- mock telegram client ---

type mockTelegramClient struct {
	getUpdatesFn func(ctx context.Context, limit int) ([]telegram.Update, error)
	getUpdateFn  func(ctx context.Context, updateID int) (*telegram.Update, error)
	ackUpdateFn  func(ctx context.Context, updateID int) error
}

func (m *mockTelegramClient) GetUpdates(ctx context.Context, limit int) ([]telegram.Update, error) {
	return m.getUpdatesFn(ctx, limit)
}

func (m *mockTelegramClient) GetUpdate(ctx context.Context, updateID int) (*telegram.Update, error) {
	return m.getUpdateFn(ctx, updateID)
}

func (m *mockTelegramClient) AckUpdate(ctx context.Context, updateID int) error {
	return m.ackUpdateFn(ctx, updateID)
}

// --- list tests ---

func TestRunTelegramList_JSON(t *testing.T) {
	updates := []telegram.Update{
		{
			UpdateID: 100,
			Message: &telegram.Message{
				MessageID: 1,
				From:      &telegram.User{ID: 42, FirstName: "John", LastName: "Doe", Username: "johndoe"},
				Chat:      telegram.Chat{ID: 42, Type: "private"},
				Date:      1700000000,
				Text:      "Hello bot",
			},
		},
	}
	client := &mockTelegramClient{
		getUpdatesFn: func(_ context.Context, limit int) ([]telegram.Update, error) {
			assert.Equal(t, 100, limit)
			return updates, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramList(context.Background(), client, &buf, 100, false, []int64{42}, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"update_id": 100`)
	assert.Contains(t, buf.String(), `"from": "John Doe"`)
	assert.Contains(t, buf.String(), `"text": "Hello bot"`)
}

func TestRunTelegramList_Table(t *testing.T) {
	updates := []telegram.Update{
		{
			UpdateID: 100,
			Message: &telegram.Message{
				MessageID: 1,
				From:      &telegram.User{ID: 42, FirstName: "John"},
				Chat:      telegram.Chat{ID: 42, Type: "private"},
				Date:      1700000000,
				Text:      "Hello bot",
			},
		},
	}
	client := &mockTelegramClient{
		getUpdatesFn: func(_ context.Context, _ int) ([]telegram.Update, error) {
			return updates, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramList(context.Background(), client, &buf, 100, true, []int64{42}, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "UPDATE ID")
	assert.Contains(t, buf.String(), "FROM")
	assert.Contains(t, buf.String(), "DATE")
	assert.Contains(t, buf.String(), "TEXT")
	assert.Contains(t, buf.String(), "100")
	assert.Contains(t, buf.String(), "John")
	assert.Contains(t, buf.String(), "Hello bot")
}

func TestRunTelegramList_Empty(t *testing.T) {
	client := &mockTelegramClient{
		getUpdatesFn: func(_ context.Context, _ int) ([]telegram.Update, error) {
			return []telegram.Update{}, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramList(context.Background(), client, &buf, 100, false, []int64{42}, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No pending messages")
}

func TestRunTelegramList_EmptyTable(t *testing.T) {
	client := &mockTelegramClient{
		getUpdatesFn: func(_ context.Context, _ int) ([]telegram.Update, error) {
			return []telegram.Update{}, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramList(context.Background(), client, &buf, 100, true, []int64{42}, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No pending messages")
}

func TestRunTelegramList_Error(t *testing.T) {
	client := &mockTelegramClient{
		getUpdatesFn: func(_ context.Context, _ int) ([]telegram.Update, error) {
			return nil, fmt.Errorf("network error")
		},
	}

	var buf bytes.Buffer
	err := runTelegramList(context.Background(), client, &buf, 100, false, []int64{42}, nil)
	assert.EqualError(t, err, "network error")
}

func TestRunTelegramList_NilMessageSkipped(t *testing.T) {
	updates := []telegram.Update{
		{UpdateID: 100, Message: nil},
		{
			UpdateID: 101,
			Message: &telegram.Message{
				MessageID: 1,
				From:      &telegram.User{ID: 42, FirstName: "Jane"},
				Chat:      telegram.Chat{ID: 42, Type: "private"},
				Date:      1700000000,
				Text:      "Real message",
			},
		},
	}
	client := &mockTelegramClient{
		getUpdatesFn: func(_ context.Context, _ int) ([]telegram.Update, error) {
			return updates, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramList(context.Background(), client, &buf, 100, false, []int64{42}, nil)
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), `"update_id": 100`)
	assert.Contains(t, buf.String(), `"update_id": 101`)
}

func TestRunTelegramList_AllowedUsersFilters(t *testing.T) {
	updates := []telegram.Update{
		{
			UpdateID: 100,
			Message: &telegram.Message{
				MessageID: 1,
				From:      &telegram.User{ID: 42, FirstName: "Allowed"},
				Chat:      telegram.Chat{ID: 42, Type: "private"},
				Date:      1700000000,
				Text:      "my message",
			},
		},
		{
			UpdateID: 101,
			Message: &telegram.Message{
				MessageID: 2,
				From:      &telegram.User{ID: 99, FirstName: "Stranger"},
				Chat:      telegram.Chat{ID: 99, Type: "private"},
				Date:      1700000060,
				Text:      "spam",
			},
		},
	}
	client := &mockTelegramClient{
		getUpdatesFn: func(_ context.Context, _ int) ([]telegram.Update, error) {
			return updates, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramList(context.Background(), client, &buf, 100, false, []int64{42}, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"update_id": 100`)
	assert.NotContains(t, buf.String(), `"update_id": 101`)
}

func TestRunTelegramList_AllowedUsersAllFiltered(t *testing.T) {
	updates := []telegram.Update{
		{
			UpdateID: 100,
			Message: &telegram.Message{
				MessageID: 1,
				From:      &telegram.User{ID: 99, FirstName: "Stranger"},
				Chat:      telegram.Chat{ID: 99, Type: "private"},
				Date:      1700000000,
				Text:      "spam",
			},
		},
	}
	client := &mockTelegramClient{
		getUpdatesFn: func(_ context.Context, _ int) ([]telegram.Update, error) {
			return updates, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramList(context.Background(), client, &buf, 100, false, []int64{42}, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No pending messages")
}

// --- get tests ---

func TestRunTelegramGet_JSON(t *testing.T) {
	update := &telegram.Update{
		UpdateID: 100,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: 42, FirstName: "John", LastName: "Doe", Username: "johndoe"},
			Chat:      telegram.Chat{ID: 42, Type: "private"},
			Date:      1700000000,
			Text:      "Hello bot",
		},
	}
	client := &mockTelegramClient{
		getUpdateFn: func(_ context.Context, updateID int) (*telegram.Update, error) {
			assert.Equal(t, 100, updateID)
			return update, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramGet(context.Background(), client, &buf, 100, false, []int64{42}, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"update_id": 100`)
	assert.Contains(t, buf.String(), `"from": "John Doe"`)
	assert.Contains(t, buf.String(), `"from_id": 42`)
	assert.Contains(t, buf.String(), `"username": "johndoe"`)
	assert.Contains(t, buf.String(), `"chat_id": 42`)
	assert.Contains(t, buf.String(), `"chat_type": "private"`)
	assert.Contains(t, buf.String(), `"text": "Hello bot"`)
}

func TestRunTelegramGet_Table(t *testing.T) {
	update := &telegram.Update{
		UpdateID: 100,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: 42, FirstName: "John", Username: "johndoe"},
			Chat:      telegram.Chat{ID: 42, Type: "private"},
			Date:      1700000000,
			Text:      "Hello bot",
		},
	}
	client := &mockTelegramClient{
		getUpdateFn: func(_ context.Context, _ int) (*telegram.Update, error) {
			return update, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramGet(context.Background(), client, &buf, 100, true, []int64{42}, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Update ID:")
	assert.Contains(t, buf.String(), "From:")
	assert.Contains(t, buf.String(), "Username:")
	assert.Contains(t, buf.String(), "100")
	assert.Contains(t, buf.String(), "John")
	assert.Contains(t, buf.String(), "johndoe")
}

func TestRunTelegramGet_Error(t *testing.T) {
	client := &mockTelegramClient{
		getUpdateFn: func(_ context.Context, _ int) (*telegram.Update, error) {
			return nil, fmt.Errorf("not found")
		},
	}

	var buf bytes.Buffer
	err := runTelegramGet(context.Background(), client, &buf, 999, false, []int64{42}, nil)
	assert.EqualError(t, err, "not found")
}

func TestRunTelegramGet_AllowedUserBlocked(t *testing.T) {
	update := &telegram.Update{
		UpdateID: 100,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: 99, FirstName: "Stranger"},
			Chat:      telegram.Chat{ID: 99, Type: "private"},
			Date:      1700000000,
			Text:      "spam",
		},
	}
	client := &mockTelegramClient{
		getUpdateFn: func(_ context.Context, _ int) (*telegram.Update, error) {
			return update, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramGet(context.Background(), client, &buf, 100, false, []int64{42}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not from an allowed (user, chat) pair")
}

func TestRunTelegramGet_AllowedUserPasses(t *testing.T) {
	update := &telegram.Update{
		UpdateID: 100,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: 42, FirstName: "Me"},
			Chat:      telegram.Chat{ID: 42, Type: "private"},
			Date:      1700000000,
			Text:      "my message",
		},
	}
	client := &mockTelegramClient{
		getUpdateFn: func(_ context.Context, _ int) (*telegram.Update, error) {
			return update, nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramGet(context.Background(), client, &buf, 100, false, []int64{42}, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"update_id": 100`)
}

// --- ack tests ---

func TestRunTelegramAck_Success(t *testing.T) {
	client := &mockTelegramClient{
		ackUpdateFn: func(_ context.Context, updateID int) error {
			assert.Equal(t, 101, updateID)
			return nil
		},
	}

	var buf bytes.Buffer
	err := runTelegramAck(context.Background(), client, &buf, 101)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Acknowledged updates up to 101")
}

func TestRunTelegramAck_Error(t *testing.T) {
	client := &mockTelegramClient{
		ackUpdateFn: func(_ context.Context, _ int) error {
			return fmt.Errorf("unauthorized")
		},
	}

	var buf bytes.Buffer
	err := runTelegramAck(context.Background(), client, &buf, 101)
	assert.EqualError(t, err, "unauthorized")
}

// --- command tree tests ---

func TestTelegramCmd_hasSubcommands(t *testing.T) {
	cmd := BuildTelegramCommands()
	subNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["list"], "expected 'list' subcommand")
	assert.True(t, subNames["get UPDATE_ID"], "expected 'get' subcommand")
	assert.True(t, subNames["ack UPDATE_ID"], "expected 'ack' subcommand")
}

// --- text truncation in table ---

func TestPrintTelegramListTable_longTextTruncated(t *testing.T) {
	longText := "This is a very long message that should be truncated when displayed in table format to keep things readable"
	summaries := []telegram.MessageSummary{
		{UpdateID: 1, MessageID: 1, From: "Test", Date: "2023-11-14T22:13:20Z", Text: longText},
	}

	var buf bytes.Buffer
	err := printTelegramListTable(&buf, summaries)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "...")
	assert.NotContains(t, buf.String(), longText)
}

func TestPrintTelegramListJSON_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printTelegramListJSON(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "null")
}

func TestPrintTelegramListTable_empty(t *testing.T) {
	var buf bytes.Buffer
	err := printTelegramListTable(&buf, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No pending messages")
}

// --- filtering tests ---

// Empty allowlist is now default-deny (matching the dispatcher). Before
// [SC-77] this branch returned every update, which was the documented
// asymmetry between CLI and daemon.
func TestFilterUpdates_emptyAllowlistDeniesAll(t *testing.T) {
	updates := []telegram.Update{
		{UpdateID: 1, Message: &telegram.Message{From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: 42, Type: "private"}}},
		{UpdateID: 2, Message: &telegram.Message{From: &telegram.User{ID: 99}, Chat: telegram.Chat{ID: 99, Type: "private"}}},
	}
	result := filterUpdates(updates, nil, nil)
	assert.Empty(t, result)
}

func TestFilterUpdates_filtersCorrectly(t *testing.T) {
	updates := []telegram.Update{
		{UpdateID: 1, Message: &telegram.Message{From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: 42, Type: "private"}}},
		{UpdateID: 2, Message: &telegram.Message{From: &telegram.User{ID: 99}, Chat: telegram.Chat{ID: 99, Type: "private"}}},
		{UpdateID: 3, Message: &telegram.Message{From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: 42, Type: "private"}}},
	}
	result := filterUpdates(updates, []int64{42}, nil)
	assert.Len(t, result, 2)
	assert.Equal(t, 1, result[0].UpdateID)
	assert.Equal(t, 3, result[1].UpdateID)
}

func TestFilterUpdates_nilMessageFiltered(t *testing.T) {
	updates := []telegram.Update{
		{UpdateID: 1, Message: nil},
		{UpdateID: 2, Message: &telegram.Message{From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: 42, Type: "private"}}},
	}
	result := filterUpdates(updates, []int64{42}, nil)
	assert.Len(t, result, 1)
	assert.Equal(t, 2, result[0].UpdateID)
}

// Allowlisted user in a group chat is filtered out unless the group ID is
// in AllowedChats — the same rule the dispatcher enforces.
func TestFilterUpdates_groupChatDefaultDenied(t *testing.T) {
	updates := []telegram.Update{
		{UpdateID: 1, Message: &telegram.Message{From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: -1001, Type: "group"}}},
	}
	result := filterUpdates(updates, []int64{42}, nil)
	assert.Empty(t, result)
}

func TestFilterUpdates_groupChatAllowedWhenOptedIn(t *testing.T) {
	updates := []telegram.Update{
		{UpdateID: 1, Message: &telegram.Message{From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: -1001, Type: "supergroup"}}},
	}
	result := filterUpdates(updates, []int64{42}, []int64{-1001})
	assert.Len(t, result, 1)
}

func TestIsAllowedUpdate_nilMessage(t *testing.T) {
	u := &telegram.Update{Message: nil}
	assert.False(t, isAllowedUpdate(u, []int64{42}, nil))
}
