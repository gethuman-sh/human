package cmddaemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
)

func TestClaudeAuthRefusals_heldUntilStoreRewritten(t *testing.T) {
	r := newClaudeAuthRefusals()
	stamp := time.Date(2026, 9, 14, 8, 58, 0, 0, time.UTC)

	assert.False(t, r.refused("s", stamp), "nothing observed yet")

	r.record("s", stamp)
	assert.True(t, r.refused("s", stamp))
	assert.True(t, r.refused("s", stamp), "reading the evidence must not consume it")
	assert.False(t, r.refused("other", stamp), "a refusal belongs to its own store")

	assert.False(t, r.refused("s", stamp.Add(time.Second)), "a rewritten store (fresh /login) retires the refusal")
	assert.False(t, r.refused("s", stamp), "and it stays retired")
}

func TestClaudeAuthRefusals_unreadableStampHeldUntilSuccess(t *testing.T) {
	r := newClaudeAuthRefusals()
	r.record("s", time.Time{})
	assert.True(t, r.refused("s", time.Time{}))
	r.clear("s")
	assert.False(t, r.refused("s", time.Time{}))
}

func TestClaudeAuthRefusals_nilIsInert(t *testing.T) {
	var r *claudeAuthRefusals
	r.record("s", time.Time{})
	r.clear("s")
	assert.False(t, r.refused("s", time.Time{}))
}

func TestParseKeychainMdat(t *testing.T) {
	attrs := `keychain: "/Users/x/Library/Keychains/login.keychain-db"
    "cdat"<timedate>=0x32303236303830343038313534335A00  "20260804081543Z\000"
    "mdat"<timedate>=0x32303236303931343038353830305A00  "20260914085800Z\000"`
	assert.Equal(t, time.Date(2026, 9, 14, 8, 58, 0, 0, time.UTC), parseKeychainMdat(attrs))
	assert.True(t, parseKeychainMdat("no such item").IsZero())
}

// SC-5036: a container store with an expired access token beside a refresh
// token is the normal resting state, and stays healthy — until a board run on
// it was refused authentication. Then the check refuses launches, and a fresh
// /login (which rewrites the store) clears it.
func TestCheckClaudeAuth_refusedRefreshTokenBlocksUntilRelogin(t *testing.T) {
	reg := claudeAuthRegistryRefresh(t, time.Now().Add(-time.Hour).UnixMilli(), "rt-rejected")
	refusals := newClaudeAuthRefusals()

	ok, _ := checkClaudeAuth(reg, refusals)
	require.True(t, ok, "no refusal observed yet: the resting state stays healthy")

	containerAuthRefusedFunc(reg, refusals)("SC-1")
	ok, detail := checkClaudeAuth(reg, refusals)
	assert.False(t, ok)
	assert.Contains(t, detail, "refresh token was rejected")
	assert.Contains(t, detail, "human agent start reauth --interactive")

	store := containerClaudeStore(reg.Entries()[0].Dir)
	later := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(store, later, later))
	ok, detail = checkClaudeAuth(reg, refusals)
	assert.True(t, ok, "a rewritten store is a fresh login")
	assert.Equal(t, "session valid", detail)
}

func TestContainerAuthRefusedFunc_unknownProjectRecordsNothing(t *testing.T) {
	reg, err := daemon.NewProjectRegistry(nil)
	require.NoError(t, err)
	refusals := newClaudeAuthRefusals()
	containerAuthRefusedFunc(reg, refusals)("SC-1")
	assert.Empty(t, refusals.byStore)
}

func TestCheckHostClaudeAuth(t *testing.T) {
	refusals := newClaudeAuthRefusals()
	stamp := time.Date(2026, 9, 14, 8, 58, 0, 0, time.UTC)

	ok, _ := checkHostClaudeAuth(refusals, stamp)
	assert.True(t, ok)

	refusals.record(hostClaudeStore, stamp)
	ok, detail := checkHostClaudeAuth(refusals, stamp)
	assert.False(t, ok)
	assert.Contains(t, detail, "/login")

	ok, _ = checkHostClaudeAuth(refusals, stamp.Add(time.Hour))
	assert.True(t, ok, "a renewed host login clears the check")
}

// The host turn is the only observer of the host login: a 401 records a
// refusal, a successful turn retires it, and an unrelated failure says nothing
// about the login either way.
func TestHostClaudeChatRunner_noteAuth(t *testing.T) {
	stamp := time.Date(2026, 9, 14, 8, 58, 0, 0, time.UTC)
	refusals := newClaudeAuthRefusals()
	r := hostClaudeChatRunner{refusals: refusals, storeStamp: func(context.Context) time.Time { return stamp }}
	ctx := context.Background()

	r.noteAuth(ctx, claudeTurnOutput{IsError: true, APIErrorStatus: 401, Result: "OAuth access token has expired"})
	assert.True(t, refusals.refused(hostClaudeStore, stamp))

	r.noteAuth(ctx, claudeTurnOutput{IsError: true, APIErrorStatus: 529})
	assert.True(t, refusals.refused(hostClaudeStore, stamp), "an overload is not evidence about the login")

	r.noteAuth(ctx, claudeTurnOutput{Result: "ok"})
	assert.False(t, refusals.refused(hostClaudeStore, stamp))
}

func TestBuildDoctorChecks_hostClaudeAuthIsAdvisory(t *testing.T) {
	c := findCheck(t, buildDoctorChecks(nil, nil, doctorPersistence{}, nil), "host-claude-auth")
	assert.False(t, c.Gating, "the host login gates no board launch")
	assert.False(t, c.Holding)
}
