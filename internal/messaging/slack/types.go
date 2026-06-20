package slack

// slackResponse is the common Slack API response envelope.
type slackResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// historyResponse wraps conversations.history.
type historyResponse struct {
	slackResponse
	Messages []APIMessage `json:"messages"`
}

// APIMessage is a raw Slack message from the API.
type APIMessage struct {
	Type  string `json:"type"`
	User  string `json:"user,omitempty"`
	Text  string `json:"text"`
	TS    string `json:"ts"`
	BotID string `json:"bot_id,omitempty"`
}

// MessageSummary is the CLI output type for the list command.
type MessageSummary struct {
	User string `json:"user"`
	Text string `json:"text"`
	TS   string `json:"ts"`
}
