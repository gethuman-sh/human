package daemon

import (
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openErrFs makes every Open fail with a non-not-exist error, so the id loader's
// "propagate a real read error" branch can be exercised deterministically.
type openErrFs struct{ afero.Fs }

func (o openErrFs) Open(name string) (afero.File, error) {
	return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrPermission}
}

func TestGenerateDaemonID(t *testing.T) {
	id, err := GenerateDaemonID()
	require.NoError(t, err)
	assert.Len(t, id, daemonIDBytes*2, "id should be hex-encoded")

	id2, err := GenerateDaemonID()
	require.NoError(t, err)
	assert.NotEqual(t, id, id2, "ids should be unique")
}

func TestDaemonIDPath(t *testing.T) {
	path := DaemonIDPath()
	assert.Contains(t, path, "human")
	assert.Contains(t, path, "daemon-id")
}

func TestLoadOrCreateDaemonID_generatesAndPersists(t *testing.T) {
	withMemFs(t)
	path := "/tmp/test/human/daemon-id"

	id, err := loadOrCreateDaemonIDAt(path)
	require.NoError(t, err)
	assert.Len(t, id, daemonIDBytes*2)

	exists, err := afero.Exists(fs, path)
	require.NoError(t, err)
	assert.True(t, exists, "id file should be written")
}

func TestLoadOrCreateDaemonID_stableAcrossCalls(t *testing.T) {
	withMemFs(t)
	path := "/tmp/test/human/daemon-id"

	id1, err := loadOrCreateDaemonIDAt(path)
	require.NoError(t, err)
	id2, err := loadOrCreateDaemonIDAt(path)
	require.NoError(t, err)

	assert.Equal(t, id1, id2, "second call should reuse the persisted id")
}

func TestLoadOrCreateDaemonID_regeneratesEmptyFile(t *testing.T) {
	withMemFs(t)
	path := "/tmp/test/human/daemon-id"

	require.NoError(t, fs.MkdirAll("/tmp/test/human", 0o700))
	require.NoError(t, afero.WriteFile(fs, path, []byte("   \n"), 0o600))

	id, err := loadOrCreateDaemonIDAt(path)
	require.NoError(t, err)
	assert.NotEmpty(t, id, "an empty/whitespace file should be regenerated")
	assert.Len(t, id, daemonIDBytes*2)
}

func TestLoadOrCreateDaemonID_readErrorPropagates(t *testing.T) {
	orig := fs
	fs = openErrFs{afero.NewMemMapFs()}
	t.Cleanup(func() { fs = orig })

	_, err := loadOrCreateDaemonIDAt("/tmp/test/human/daemon-id")
	require.Error(t, err, "a non-not-exist read error must propagate, never silently overwrite")
}
