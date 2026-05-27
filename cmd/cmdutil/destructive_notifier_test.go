package cmdutil

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// mockNotifier records calls to Notify.
type mockNotifier struct {
	calls []mockNotifyCall
	err   error // error to return from Notify
}

type mockNotifyCall struct {
	chatID int64
	text   string
}

func (m *mockNotifier) Notify(_ context.Context, chatID int64, text string) error {
	m.calls = append(m.calls, mockNotifyCall{chatID: chatID, text: text})
	return m.err
}

func TestDispatchDestructiveNotifier_NotifyDestructive(t *testing.T) {
	mock := &mockNotifier{}
	notifier := &DispatchDestructiveNotifier{
		Notifier: mock,
		ChatID:   42,
	}

	entry := tracker.DestructiveEntry{
		Timestamp: "2026-01-01T00:00:00Z",
		Operation: "DeleteIssue",
		Tracker:   "work",
		Kind:      "jira",
		Key:       "KAN-5",
	}

	notifier.NotifyDestructive(context.Background(), entry)

	require.Len(t, mock.calls, 1)
	assert.Equal(t, int64(42), mock.calls[0].chatID)
	assert.Contains(t, mock.calls[0].text, "DeleteIssue")
	assert.Contains(t, mock.calls[0].text, "KAN-5")
}

func TestFormatDestructiveMessage_Delete(t *testing.T) {
	entry := tracker.DestructiveEntry{
		Operation: "DeleteIssue",
		Tracker:   "work",
		Kind:      "jira",
		Key:       "KAN-5",
	}
	msg := FormatDestructiveMessage(entry)
	assert.Equal(t, "[DESTRUCTIVE] DeleteIssue on KAN-5 (tracker: work/jira)", msg)
}

func TestFormatDestructiveMessage_Transition(t *testing.T) {
	entry := tracker.DestructiveEntry{
		Operation: "TransitionIssue",
		Tracker:   "work",
		Kind:      "jira",
		Key:       "KAN-5",
		Detail:    "-> Done",
	}
	msg := FormatDestructiveMessage(entry)
	assert.Equal(t, "[DESTRUCTIVE] TransitionIssue on KAN-5 (tracker: work/jira) -> Done", msg)
}

func TestFormatDestructiveMessage_Edit(t *testing.T) {
	entry := tracker.DestructiveEntry{
		Operation: "EditIssue",
		Tracker:   "work",
		Kind:      "linear",
		Key:       "HUM-10",
		Detail:    "changed: title, description",
	}
	msg := FormatDestructiveMessage(entry)
	assert.Equal(t, "[DESTRUCTIVE] EditIssue on HUM-10 (tracker: work/linear) changed: title, description", msg)
}

func TestFormatDestructiveMessage_WithError(t *testing.T) {
	entry := tracker.DestructiveEntry{
		Operation: "DeleteIssue",
		Tracker:   "work",
		Kind:      "jira",
		Key:       "KAN-5",
		Error:     "forbidden",
	}
	msg := FormatDestructiveMessage(entry)
	assert.Contains(t, msg, "[error: forbidden]")
}

func TestDispatchDestructiveNotifier_ErrorIgnored(t *testing.T) {
	mock := &mockNotifier{err: fmt.Errorf("network error")}
	notifier := &DispatchDestructiveNotifier{
		Notifier: mock,
		ChatID:   42,
	}

	entry := tracker.DestructiveEntry{
		Operation: "DeleteIssue",
		Tracker:   "work",
		Kind:      "jira",
		Key:       "KAN-5",
	}

	// Should not panic despite error
	notifier.NotifyDestructive(context.Background(), entry)

	require.Len(t, mock.calls, 1)
}
