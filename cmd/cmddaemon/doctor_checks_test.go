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
	t.Helper()
	dir := t.TempDir()
	credDir := filepath.Join(dir, ".devcontainer", "claude")
	require.NoError(t, os.MkdirAll(credDir, 0o755))
	body := `{"claudeAiOauth":{}}`
	if expiresAtMS != 0 {
		body = fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":%d}}`, expiresAtMS)
	}
	require.NoError(t, os.WriteFile(filepath.Join(credDir, ".credentials.json"), []byte(body), 0o600))
	reg, err := daemon.NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	return reg
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
	assert.Contains(t, detail, "re-authenticate Claude")
}

// A session whose expiresAt is in the future is fresh — the daemon may serve work.
func TestCheckClaudeAuth_valid(t *testing.T) {
	reg := claudeAuthRegistry(t, time.Now().Add(time.Hour).UnixMilli())
	ok, detail := checkClaudeAuth(reg)
	assert.True(t, ok)
	assert.Equal(t, "session valid", detail)
}

// No host credential store → fail-open (ok=true): a path/schema mismatch must
// degrade to pre-fix behaviour, never block a healthy daemon.
func TestCheckClaudeAuth_absentStoreFailsOpen(t *testing.T) {
	reg, err := daemon.NewProjectRegistry([]string{t.TempDir()})
	require.NoError(t, err)
	ok, _ := checkClaudeAuth(reg)
	assert.True(t, ok)
}

// A present credential file that records no expiry cannot be judged, so the
// check fails open rather than blocking on an unknowable freshness.
func TestCheckClaudeAuth_missingExpiryFailsOpen(t *testing.T) {
	reg := claudeAuthRegistry(t, 0)
	ok, _ := checkClaudeAuth(reg)
	assert.True(t, ok)
}
