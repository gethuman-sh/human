package cmddaemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMonarchAddr_flagThenConfig(t *testing.T) {
	t.Run("flag wins", func(t *testing.T) {
		dir := t.TempDir()
		writeMonarchConfig(t, dir, "config-host:1234", "alpha")
		addr, team := resolveMonarchAddr("flag-host:9999", dir)
		assert.Equal(t, "flag-host:9999", addr)
		assert.Equal(t, "", team, "flag carries no team label")
	})

	t.Run("config fallback", func(t *testing.T) {
		dir := t.TempDir()
		writeMonarchConfig(t, dir, "config-host:1234", "alpha")
		addr, team := resolveMonarchAddr("", dir)
		assert.Equal(t, "config-host:1234", addr)
		assert.Equal(t, "alpha", team)
	})

	t.Run("neither configured", func(t *testing.T) {
		dir := t.TempDir()
		addr, team := resolveMonarchAddr("", dir)
		assert.Equal(t, "", addr)
		assert.Equal(t, "", team)
	})
}

func writeMonarchConfig(t *testing.T, dir, addr, team string) {
	t.Helper()
	content := "monarch:\n  addr: " + addr + "\n  team: " + team + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o600))
}
