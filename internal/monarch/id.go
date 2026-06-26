package monarch

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/gethuman-sh/human/errors"
)

// fs is the filesystem used by id operations. Tests swap this with
// afero.NewMemMapFs() so the persisted-id behaviour is exercised without
// touching the real home directory.
var fs afero.Fs = afero.NewOsFs()

// DaemonIDPath returns ~/.human/monarch-id.
func DaemonIDPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "monarch-id")
	}
	return filepath.Join(home, ".human", "monarch-id")
}

// LoadOrCreateDaemonID reads the persisted opaque id or creates one. The id is
// random and identity-free — never derived from hostname, user, or the secret
// daemon token.
func LoadOrCreateDaemonID() (string, error) { return loadOrCreateDaemonIDAt(DaemonIDPath()) }

func loadOrCreateDaemonIDAt(path string) (string, error) {
	data, err := afero.ReadFile(fs, path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
		// Empty/whitespace file — fall through to regenerate.
	} else if !os.IsNotExist(err) {
		// A read error other than "not found" (permission denied, NFS stall, …)
		// must propagate so we never silently replace an id the user can't read.
		return "", errors.WrapWithDetails(err, "reading monarch daemon id", "path", path)
	}

	id, err := NewDaemonID()
	if err != nil {
		return "", err
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", errors.WrapWithDetails(err, "creating monarch id directory", "path", path)
	}
	if err := afero.WriteFile(fs, path, []byte(id), 0o600); err != nil {
		return "", errors.WrapWithDetails(err, "writing monarch daemon id", "path", path)
	}
	return id, nil
}
