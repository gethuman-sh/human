package cmdslack

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/messaging/slack"
)

// --- mock slack client ---

type mockSlackClient struct {
	sendMessageFn  func(ctx context.Context, text string) error
	listMessagesFn func(ctx context.Context, limit int) ([]slack.MessageSummary, error)
}

func (m *mockSlackClient) SendMessage(ctx context.Context, text string) error {
	return m.sendMessageFn(ctx, text)
}

func (m *mockSlackClient) ListMessages(ctx context.Context, limit int) ([]slack.MessageSummary, error) {
	return m.listMessagesFn(ctx, limit)
}

// --- send tests ---

func TestRunSlackSend_happy(t *testing.T) {
	var sentText string
	client := &mockSlackClient{
		sendMessageFn: func(_ context.Context, text string) error {
			sentText = text
			return nil
		},
	}

	var buf bytes.Buffer
	err := runSlackSend(context.Background(), client, &buf, "Hello Slack")
	require.NoError(t, err)
	assert.Equal(t, "Hello Slack", sentText)
	assert.Contains(t, buf.String(), "Message sent")
}

func TestRunSlackSend_error(t *testing.T) {
	client := &mockSlackClient{
		sendMessageFn: func(_ context.Context, _ string) error {
			return fmt.Errorf("channel_not_found")
		},
	}

	var buf bytes.Buffer
	err := runSlackSend(context.Background(), client, &buf, "test")
	assert.EqualError(t, err, "channel_not_found")
}

// --- list tests ---

func TestRunSlackList_JSON(t *testing.T) {
	msgs := []slack.MessageSummary{
		{User: "U123", Text: "Hello", TS: "1700000000.000100"},
		{User: "U456", Text: "World", TS: "1700000060.000200"},
	}
	client := &mockSlackClient{
		listMessagesFn: func(_ context.Context, limit int) ([]slack.MessageSummary, error) {
			assert.Equal(t, 20, limit)
			return msgs, nil
		},
	}

	var buf bytes.Buffer
	err := runSlackList(context.Background(), client, &buf, 20, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"user": "U123"`)
	assert.Contains(t, buf.String(), `"text": "Hello"`)
	assert.Contains(t, buf.String(), `"text": "World"`)
}

func TestRunSlackList_Table(t *testing.T) {
	msgs := []slack.MessageSummary{
		{User: "U123", Text: "Hello", TS: "1700000000.000100"},
	}
	client := &mockSlackClient{
		listMessagesFn: func(_ context.Context, _ int) ([]slack.MessageSummary, error) {
			return msgs, nil
		},
	}

	var buf bytes.Buffer
	err := runSlackList(context.Background(), client, &buf, 20, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "USER")
	assert.Contains(t, buf.String(), "TS")
	assert.Contains(t, buf.String(), "TEXT")
	assert.Contains(t, buf.String(), "U123")
	assert.Contains(t, buf.String(), "Hello")
}

func TestRunSlackList_Empty(t *testing.T) {
	client := &mockSlackClient{
		listMessagesFn: func(_ context.Context, _ int) ([]slack.MessageSummary, error) {
			return []slack.MessageSummary{}, nil
		},
	}

	var buf bytes.Buffer
	err := runSlackList(context.Background(), client, &buf, 20, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No messages")
}

func TestRunSlackList_Error(t *testing.T) {
	client := &mockSlackClient{
		listMessagesFn: func(_ context.Context, _ int) ([]slack.MessageSummary, error) {
			return nil, fmt.Errorf("network error")
		},
	}

	var buf bytes.Buffer
	err := runSlackList(context.Background(), client, &buf, 20, false)
	assert.EqualError(t, err, "network error")
}

func TestRunSlackList_TableLongTextTruncated(t *testing.T) {
	longText := "This is a very long message that should be truncated when displayed in table format to keep things readable"
	msgs := []slack.MessageSummary{
		{User: "U123", Text: longText, TS: "1700000000.000100"},
	}
	client := &mockSlackClient{
		listMessagesFn: func(_ context.Context, _ int) ([]slack.MessageSummary, error) {
			return msgs, nil
		},
	}

	var buf bytes.Buffer
	err := runSlackList(context.Background(), client, &buf, 20, true)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "...")
	assert.NotContains(t, buf.String(), longText)
}

// --- command tree tests ---

func TestSlackCmd_hasSubcommands(t *testing.T) {
	cmd := BuildSlackCommands()
	subNames := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subNames[sub.Use] = true
	}
	assert.True(t, subNames["send MESSAGE"], "expected 'send' subcommand")
	assert.True(t, subNames["list"], "expected 'list' subcommand")
}
