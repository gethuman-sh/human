package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/apiclient"
)

// Client is a Telegram Bot API client.
type Client struct {
	api   *apiclient.Client
	token string
}

// New creates a Telegram client with the given bot token.
func New(token string) *Client {
	return newWithBaseURL("https://api.telegram.org", token)
}

// newWithBaseURL creates a Telegram client with a custom base URL (for testing).
func newWithBaseURL(baseURL, token string) *Client {
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.NoAuth()),
			apiclient.WithURLBuilder(apiclient.ParsePathURL()),
			apiclient.WithProviderName("telegram"),
			apiclient.WithPathSanitizer(func(p string) string {
				return sanitizeTokenInPath(p, token)
			}),
			apiclient.WithErrorFormatter(func(_, method, path string, statusCode int, body []byte) error {
				sanitizedPath := sanitizeTokenInPath(path, token)
				return errors.WithDetails(
					fmt.Sprintf("telegram %s %s returned %d: %s", method, sanitizedPath, statusCode, string(body)),
					"statusCode", statusCode, "method", method)
			}),
		),
		token: token,
	}
}

// SetHTTPDoer replaces the HTTP client used for API requests.
func (c *Client) SetHTTPDoer(doer apiclient.HTTPDoer) {
	c.api.SetHTTPDoer(doer)
}

// GetUpdates fetches pending updates from the Telegram Bot API.
// It does not pass an offset, so this is a read-only peek at pending messages.
func (c *Client) GetUpdates(ctx context.Context, limit int) ([]Update, error) {
	// Escape both the token and the allowed_updates literal so tokens
	// containing whitespace, slashes, or URL-reserved characters cannot
	// produce a malformed path. The Bot API only supports URL auth, so
	// the token must stay in the path — escaping is the best we can do.
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("allowed_updates", `["message"]`)
	path := fmt.Sprintf("/bot%s/getUpdates?%s", url.PathEscape(c.token), q.Encode())
	resp, err := c.doRequest(ctx, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	var result getUpdatesResponse
	if err := apiclient.DecodeJSON(resp, &result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, errors.WithDetails(
			fmt.Sprintf("Telegram API error: %s", result.Description))
	}
	return result.Result, nil
}

// GetUpdate fetches all pending updates and returns the one matching updateID.
// Returns an error if the update is not found among pending updates.
func (c *Client) GetUpdate(ctx context.Context, updateID int) (*Update, error) {
	updates, err := c.GetUpdates(ctx, 100)
	if err != nil {
		return nil, err
	}
	for i := range updates {
		if updates[i].UpdateID == updateID {
			return &updates[i], nil
		}
	}
	return nil, errors.WithDetails(
		fmt.Sprintf("update %d not found in pending updates", updateID),
		"updateID", updateID)
}

// AckUpdate acknowledges all updates up to and including updateID by calling
// getUpdates with offset = updateID + 1. This permanently removes those
// updates from the pending queue.
func (c *Client) AckUpdate(ctx context.Context, updateID int) error {
	path := fmt.Sprintf("/bot%s/getUpdates?offset=%d&limit=0", url.PathEscape(c.token), updateID+1)
	resp, err := c.doRequest(ctx, http.MethodGet, path)
	if err != nil {
		return err
	}
	var result getUpdatesResponse
	if err := apiclient.DecodeJSON(resp, &result); err != nil {
		return err
	}
	if !result.OK {
		return errors.WithDetails(
			fmt.Sprintf("Telegram API error: %s", result.Description))
	}
	return nil
}

// SendMessage sends a text message to the given chat via the Telegram Bot API.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	path := fmt.Sprintf("/bot%s/sendMessage", url.PathEscape(c.token))
	body, err := json.Marshal(struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}{ChatID: chatID, Text: text})
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling sendMessage request")
	}
	resp, err := c.api.Do(ctx, http.MethodPost, path, "", bytes.NewReader(body))
	if err != nil {
		return err
	}
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description,omitempty"`
	}
	if err := apiclient.DecodeJSON(resp, &result); err != nil {
		return err
	}
	if !result.OK {
		return errors.WithDetails(
			fmt.Sprintf("Telegram sendMessage error: %s", result.Description))
	}
	return nil
}

func (c *Client) doRequest(ctx context.Context, method, path string) (*http.Response, error) {
	return c.api.Do(ctx, method, path, "", nil)
}

// sanitizeTokenInPath replaces the bot token in a path with "bot<REDACTED>".
func sanitizeTokenInPath(path, token string) string {
	if token == "" {
		return path
	}
	return strings.ReplaceAll(path, token, "<REDACTED>")
}
