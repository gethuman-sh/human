package cmddaemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
