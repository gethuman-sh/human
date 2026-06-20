package telegram

// getUpdatesResponse is the top-level response from the Telegram Bot API getUpdates endpoint.
type getUpdatesResponse struct {
	OK          bool     `json:"ok"`
	Result      []Update `json:"result"`
	Description string   `json:"description,omitempty"`
}

// Update represents a single update from the Telegram Bot API.
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

// Message represents a Telegram message.
type Message struct {
	MessageID int    `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
}

// User represents a Telegram user.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// Chat represents a Telegram chat.
type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title,omitempty"`
}

// MessageSummary is the CLI output type for the list command.
type MessageSummary struct {
	UpdateID  int    `json:"update_id"`
	MessageID int    `json:"message_id"`
	From      string `json:"from"`
	Date      string `json:"date"`
	Text      string `json:"text"`
}

// MessageDetail is the CLI output type for the get command.
type MessageDetail struct {
	UpdateID  int    `json:"update_id"`
	MessageID int    `json:"message_id"`
	From      string `json:"from"`
	FromID    int64  `json:"from_id"`
	Username  string `json:"username"`
	ChatID    int64  `json:"chat_id"`
	ChatType  string `json:"chat_type"`
	Date      string `json:"date"`
	Text      string `json:"text"`
}
