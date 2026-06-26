package monarch

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDaemonIDPath(t *testing.T) {
	assert.True(t, strings.HasSuffix(DaemonIDPath(), "monarch-id"))
}

func TestLoadOrCreateDaemonID_public(t *testing.T) {
	prev := fs
	fs = afero.NewMemMapFs()
	t.Cleanup(func() { fs = prev })

	id, err := LoadOrCreateDaemonID()
	require.NoError(t, err)
	assert.Regexp(t, `^daemon-[0-9a-f]{8}$`, id)
}

func TestLoadOrCreateDaemonID_stable(t *testing.T) {
	prev := fs
	fs = afero.NewMemMapFs()
	t.Cleanup(func() { fs = prev })

	const path = "/home/test/.human/monarch-id"

	id1, err := loadOrCreateDaemonIDAt(path)
	require.NoError(t, err)
	assert.Regexp(t, `^daemon-[0-9a-f]{8}$`, id1)

	id2, err := loadOrCreateDaemonIDAt(path)
	require.NoError(t, err)
	assert.Equal(t, id1, id2, "id must be stable across calls")

	info, err := fs.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestLoadOrCreateDaemonID_regeneratesOnEmpty(t *testing.T) {
	prev := fs
	fs = afero.NewMemMapFs()
	t.Cleanup(func() { fs = prev })

	const path = "/home/test/.human/monarch-id"
	require.NoError(t, afero.WriteFile(fs, path, []byte("   \n"), 0o600))

	id, err := loadOrCreateDaemonIDAt(path)
	require.NoError(t, err)
	assert.Regexp(t, `^daemon-[0-9a-f]{8}$`, id)
}
