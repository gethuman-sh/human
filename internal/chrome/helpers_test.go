package chrome

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// shortTempDir creates a temporary directory under /tmp with a short path.
// On macOS, t.TempDir() produces paths under /var/folders/... that can exceed
// the 104-character Unix socket path limit, causing bind failures. Using /tmp
// as the base guarantees paths short enough for Unix socket filenames.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hum-test-")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}
