package telegram

// Chat type constants as returned by the Telegram Bot API in the Chat.Type
// field. See https://core.telegram.org/bots/api#chat.
const (
	ChatTypePrivate    = "private"
	ChatTypeGroup      = "group"
	ChatTypeSupergroup = "supergroup"
	ChatTypeChannel    = "channel"
)

// AuthzReason describes why a message was accepted or rejected by IsAllowed.
// Rejections use a stable string form suitable for structured logging and
// metrics, so operators can distinguish probing attempts (NotInAllowlist)
// from misconfigured group-chat dispatch (GroupChatDenied).
type AuthzReason string

const (
	// AuthzAllowed means the user and chat pass all access checks.
	AuthzAllowed AuthzReason = "allowed"
	// AuthzNotInAllowlist means the user is not in AllowedUsers, or there
	// is no allowlist configured at all (default-deny).
	AuthzNotInAllowlist AuthzReason = "not_in_allowlist"
	// AuthzGroupChatDenied means the user is allowlisted but the message
	// came from a group, supergroup, or channel that is not in AllowedChats.
	// Group dispatch is default-deny and must be opted in per chat.
	AuthzGroupChatDenied AuthzReason = "group_chat_denied"
	// AuthzUnknownChatType means the chat type is not one of the four
	// documented values. Future-proofing: deny unknown chat types.
	AuthzUnknownChatType AuthzReason = "unknown_chat_type"
)

// IsAllowed decides whether a (user, chat) pair is allowed to dispatch.
//
// The rule, in order:
//
//  1. Empty allowedUsers → deny. (Default-deny, the invariant established
//     by [SC-71] commit 3e6acdb.)
//  2. User not in allowedUsers → deny with reason NotInAllowlist.
//  3. Private chat (1:1 with the bot) → allow. The chat ID equals the user
//     ID in a private chat, so AllowedChats need not list it.
//  4. Group / supergroup / channel chat → deny unless chat.ID is in
//     allowedChats. Group dispatch is a distinct trust surface from
//     private DMs and must be opted in per chat.
//  5. Any other chat type → deny with reason UnknownChatType.
//
// A nil user is always denied (malformed update).
func IsAllowed(user *User, chat Chat, allowedUsers, allowedChats []int64) (bool, AuthzReason) {
	if len(allowedUsers) == 0 {
		return false, AuthzNotInAllowlist
	}
	if user == nil {
		return false, AuthzNotInAllowlist
	}
	if !containsInt64(allowedUsers, user.ID) {
		return false, AuthzNotInAllowlist
	}

	switch chat.Type {
	case ChatTypePrivate:
		return true, AuthzAllowed
	case ChatTypeGroup, ChatTypeSupergroup, ChatTypeChannel:
		if containsInt64(allowedChats, chat.ID) {
			return true, AuthzAllowed
		}
		return false, AuthzGroupChatDenied
	default:
		return false, AuthzUnknownChatType
	}
}

func containsInt64(haystack []int64, needle int64) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
