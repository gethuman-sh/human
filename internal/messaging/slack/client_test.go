package slack

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient creates a Slack client pointing at a test server.
func newTestClient(baseURL, token, channel string) *Client {
	return newWithBaseURL(baseURL, token, channel)
}

func TestSendMessage_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/chat.postMessage", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		_, _ = fmt.Fprint(w, `{"ok": true}`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "test-token", "C123")
	err := client.SendMessage(context.Background(), "Hello from daemon")
	require.NoError(t, err)
}

func TestSendMessage_apiError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok": false, "error": "channel_not_found"}`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "test-token", "C999")
	err := client.SendMessage(context.Background(), "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel_not_found")
}

func TestSendMessage_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"ok": false, "error": "invalid_auth"}`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "bad-token", "C123")
	err := client.SendMessage(context.Background(), "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestListMessages_happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/conversations.history", r.URL.Path)
		assert.Equal(t, "C123", r.URL.Query().Get("channel"))
		assert.Equal(t, "10", r.URL.Query().Get("limit"))
		_, _ = fmt.Fprint(w, `{
			"ok": true,
			"messages": [
				{"type": "message", "user": "U123", "text": "Hello", "ts": "1700000000.000100"},
				{"type": "message", "user": "U456", "text": "World", "ts": "1700000060.000200", "bot_id": "B789"}
			]
		}`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "test-token", "C123")
	msgs, err := client.ListMessages(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, msgs, 2)

	assert.Equal(t, "U123", msgs[0].User)
	assert.Equal(t, "Hello", msgs[0].Text)
	assert.Equal(t, "1700000000.000100", msgs[0].TS)

	assert.Equal(t, "U456", msgs[1].User)
	assert.Equal(t, "World", msgs[1].Text)
}

func TestListMessages_empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok": true, "messages": []}`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "test-token", "C123")
	msgs, err := client.ListMessages(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

func TestListMessages_apiError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok": false, "error": "channel_not_found"}`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "test-token", "C999")
	_, err := client.ListMessages(context.Background(), 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel_not_found")
}

func TestListMessages_limitClamping(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected string
	}{
		{"zero becomes 100", 0, "100"},
		{"negative becomes 100", -5, "100"},
		{"over 999 becomes 999", 1500, "999"},
		{"normal value passes through", 50, "50"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, tc.expected, r.URL.Query().Get("limit"))
				_, _ = fmt.Fprint(w, `{"ok": true, "messages": []}`)
			}))
			defer srv.Close()

			client := newTestClient(srv.URL, "test-token", "C123")
			_, err := client.ListMessages(context.Background(), tc.input)
			require.NoError(t, err)
		})
	}
}

func TestSetHTTPDoer(t *testing.T) {
	client := newTestClient("http://unused", "token", "C123")
	// Verify SetHTTPDoer doesn't panic with a valid doer.
	client.SetHTTPDoer(http.DefaultClient)
}
