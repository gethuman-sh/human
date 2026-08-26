package cmddaemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
)

// claudeAuthRegistry writes a bind-mounted Claude credential file with the given
// expiry (epoch-ms) under a temp project dir and returns a registry over it. A
// zero expiry writes no expiresAt field, exercising the missing-expiry path.
func claudeAuthRegistry(t *testing.T, expiresAtMS int64) *daemon.ProjectRegistry {
	return claudeAuthRegistryRefresh(t, expiresAtMS, "")
}

func claudeAuthRegistryRefresh(t *testing.T, expiresAtMS int64, refreshToken string) *daemon.ProjectRegistry {
	t.Helper()
	dir := t.TempDir()
	credDir := filepath.Join(dir, ".devcontainer", "claude")
	require.NoError(t, os.MkdirAll(credDir, 0o755))
	body := `{"claudeAiOauth":{}}`
	if expiresAtMS != 0 {
		body = fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":%d,"refreshToken":%q}}`, expiresAtMS, refreshToken)
	}
	require.NoError(t, os.WriteFile(filepath.Join(credDir, ".credentials.json"), []byte(body), 0o600))
	reg, err := daemon.NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	return reg
}

// buildDoctorChecks must classify every probe: the gating set is the launch
// gate's own critical list (one source of truth), and the credential check —
// the one that reaches out to tracker APIs/vault — is transient, so a blip is
// never raised as a system fault (SC-1991).
func TestBuildDoctorChecks_Classification(t *testing.T) {
	reg, err := daemon.NewProjectRegistry([]string{t.TempDir()})
	require.NoError(t, err)

	checks := buildDoctorChecks(reg, nil, doctorPersistence{})

	byID := make(map[string]daemon.DoctorCheckDef, len(checks))
	for _, c := range checks {
		byID[c.ID] = c
	}

	gating := make(map[string]bool, len(daemon.LaunchCriticalChecks))
	for _, id := range daemon.LaunchCriticalChecks {
		gating[id] = true
	}
	for id, def := range byID {
		assert.Equalf(t, gating[id], def.Gating, "gating flag for %q must match LaunchCriticalChecks", id)
	}

	assert.True(t, byID["trackers"].Transient, "the credential check must be transient")
	assert.False(t, byID["ca-cert"].Transient, "a local-file check must not be transient")
	assert.True(t, byID["docker"].Gating)
	assert.False(t, byID["trackers"].Gating, "the credential check gates nothing")
}

// An absent CA is a fresh install (generated on first proxy use) — only a
// present-but-unparseable file is the ticket-428 failure that must go red.
func TestCheckCACert(t *testing.T) {
	ok, detail := checkCACert(filepath.Join(t.TempDir(), "missing.crt"))
	assert.True(t, ok)
	assert.Equal(t, "not yet generated", detail)

	bogus := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(bogus, []byte("not a pem"), 0o600))
	ok, detail = checkCACert(bogus)
	assert.False(t, ok)
	assert.Contains(t, detail, "restart the daemon to regenerate")
}

func TestCheckPersistence(t *testing.T) {
	ok, _ := checkPersistence(doctorPersistence{stats: true, audit: true, confirms: true})
	assert.True(t, ok)

	ok, detail := checkPersistence(doctorPersistence{stats: false, audit: true, confirms: true})
	assert.False(t, ok)
	assert.Contains(t, detail, "stats")
	assert.NotContains(t, detail, "audit,")

	ok, detail = checkPersistence(doctorPersistence{stats: true, audit: true, confirms: false})
	assert.False(t, ok)
	assert.Contains(t, detail, "approvals")
}

// A session whose expiresAt is in the past is the SC-912 failure: the check goes
// red and names the re-authenticate fix so the daemon stops sniping board work.
func TestCheckClaudeAuth_expired(t *testing.T) {
	reg := claudeAuthRegistry(t, time.Now().Add(-time.Hour).UnixMilli())
	ok, detail := checkClaudeAuth(reg)
	assert.False(t, ok)
	assert.Contains(t, detail, "container credential store")
}

// A session whose expiresAt is in the future is fresh — the daemon may serve work.
func TestCheckClaudeAuth_valid(t *testing.T) {
	reg := claudeAuthRegistry(t, time.Now().Add(time.Hour).UnixMilli())
	ok, detail := checkClaudeAuth(reg)
	assert.True(t, ok)
	assert.Equal(t, "session valid", detail)
}

// A registered project with no host credential store at all is the SC-4686
// failure: every stage agent dies ~7s in at "Not logged in", while doctor said
// "session valid". Absence is not schema drift — it is a definite state with a
// known remedy, so it goes red and names it.
func TestCheckClaudeAuth_absentStoreIsUnauthenticated(t *testing.T) {
	dir := t.TempDir()
	reg, err := daemon.NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	ok, detail := checkClaudeAuth(reg)
	assert.False(t, ok)
	assert.Contains(t, detail, dir)
	assert.Contains(t, detail, "container credential store")
	assert.Contains(t, detail, "human agent start reauth --interactive")
}

// A store that cannot be read for a reason other than absence is unjudgeable —
// the daemon has no evidence the session is dead — so it keeps failing open.
// Reading a directory yields a non-NotExist error on every supported platform.
func TestCheckClaudeAuth_unreadableStoreFailsOpen(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".devcontainer", "claude", ".credentials.json"), 0o755))
	reg, err := daemon.NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	ok, detail := checkClaudeAuth(reg)
	assert.True(t, ok)
	assert.Equal(t, "session valid", detail)
}

// Schema drift must never block a healthy daemon: a store whose JSON no longer
// parses is reported ok, exactly as before SC-4686.
func TestCheckClaudeAuth_unparseableStoreFailsOpen(t *testing.T) {
	dir := t.TempDir()
	credDir := filepath.Join(dir, ".devcontainer", "claude")
	require.NoError(t, os.MkdirAll(credDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(credDir, ".credentials.json"), []byte("not json at all"), 0o600))
	reg, err := daemon.NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	ok, detail := checkClaudeAuth(reg)
	assert.True(t, ok)
	assert.Equal(t, "session valid", detail)
}

// A present credential file that records no expiry cannot be judged, so the
// check fails open rather than blocking on an unknowable freshness. This is
// also the shape schema drift takes — a renamed field leaves the block looking
// empty — which is why an empty block is NOT treated like an absent store.
func TestCheckClaudeAuth_missingExpiryFailsOpen(t *testing.T) {
	reg := claudeAuthRegistry(t, 0)
	ok, _ := checkClaudeAuth(reg)
	assert.True(t, ok)
}

// An expired ACCESS token with a refresh token present is the normal resting
// state between agent runs — the next launched Claude renews it. The check
// must not block launches (the "but Claude IS authenticated" false positive).
func TestCheckClaudeAuth_expiredAccessTokenWithRefreshTokenIsHealthy(t *testing.T) {
	reg := claudeAuthRegistryRefresh(t, time.Now().Add(-time.Hour).UnixMilli(), "rt-present")
	ok, detail := checkClaudeAuth(reg)
	assert.True(t, ok)
	assert.Equal(t, "session valid", detail)
}
