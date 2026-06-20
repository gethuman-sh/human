package telegram

import "testing"

func TestIsAllowed(t *testing.T) {
	alice := &User{ID: 42, FirstName: "Alice"}
	bob := &User{ID: 99, FirstName: "Bob"}
	privateChat := Chat{ID: 42, Type: ChatTypePrivate}
	groupChat := Chat{ID: -1001, Type: ChatTypeGroup}
	superGroup := Chat{ID: -1002, Type: ChatTypeSupergroup}
	channel := Chat{ID: -1003, Type: ChatTypeChannel}
	weirdChat := Chat{ID: 7, Type: "something_new"}

	cases := []struct {
		name         string
		user         *User
		chat         Chat
		allowedUsers []int64
		allowedChats []int64
		wantOK       bool
		wantReason   AuthzReason
	}{
		{
			name:         "empty allowlist denies even a would-be allowed user",
			user:         alice,
			chat:         privateChat,
			allowedUsers: nil,
			wantOK:       false,
			wantReason:   AuthzNotInAllowlist,
		},
		{
			name:         "nil user is denied",
			user:         nil,
			chat:         privateChat,
			allowedUsers: []int64{42},
			wantOK:       false,
			wantReason:   AuthzNotInAllowlist,
		},
		{
			name:         "unknown user is denied",
			user:         bob,
			chat:         privateChat,
			allowedUsers: []int64{42},
			wantOK:       false,
			wantReason:   AuthzNotInAllowlist,
		},
		{
			name:         "allowed user in private chat is accepted",
			user:         alice,
			chat:         privateChat,
			allowedUsers: []int64{42},
			wantOK:       true,
			wantReason:   AuthzAllowed,
		},
		{
			name:         "allowed user in group chat without AllowedChats is denied",
			user:         alice,
			chat:         groupChat,
			allowedUsers: []int64{42},
			wantOK:       false,
			wantReason:   AuthzGroupChatDenied,
		},
		{
			name:         "allowed user in group chat listed in AllowedChats is accepted",
			user:         alice,
			chat:         groupChat,
			allowedUsers: []int64{42},
			allowedChats: []int64{-1001},
			wantOK:       true,
			wantReason:   AuthzAllowed,
		},
		{
			name:         "allowed user in supergroup listed in AllowedChats is accepted",
			user:         alice,
			chat:         superGroup,
			allowedUsers: []int64{42},
			allowedChats: []int64{-1002},
			wantOK:       true,
			wantReason:   AuthzAllowed,
		},
		{
			name:         "allowed user in channel not listed is denied",
			user:         alice,
			chat:         channel,
			allowedUsers: []int64{42},
			allowedChats: []int64{-1001}, // different chat
			wantOK:       false,
			wantReason:   AuthzGroupChatDenied,
		},
		{
			name:         "allowlisted chat without allowlisted user is still denied",
			user:         bob,
			chat:         groupChat,
			allowedUsers: []int64{42},
			allowedChats: []int64{-1001},
			wantOK:       false,
			wantReason:   AuthzNotInAllowlist,
		},
		{
			name:         "unknown chat type denies even for allowed users",
			user:         alice,
			chat:         weirdChat,
			allowedUsers: []int64{42},
			allowedChats: []int64{7},
			wantOK:       false,
			wantReason:   AuthzUnknownChatType,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := IsAllowed(tc.user, tc.chat, tc.allowedUsers, tc.allowedChats)
			if ok != tc.wantOK {
				t.Errorf("IsAllowed ok = %v, want %v", ok, tc.wantOK)
			}
			if reason != tc.wantReason {
				t.Errorf("IsAllowed reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}
