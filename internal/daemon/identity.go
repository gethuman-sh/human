package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/gethuman-sh/human/errors"
)

// daemonIDBytes is the random length of an auto-generated daemon id. Short
// enough to read in a tracker comment, long enough to be collision-free across
// a team.
const daemonIDBytes = 4 // 8 hex chars

// GenerateDaemonID returns a short cryptographically random hex id.
func GenerateDaemonID() (string, error) {
	b := make([]byte, daemonIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// DaemonIDPath returns the default path for the persisted daemon id, alongside
// the daemon token so both share the host's per-user config dir.
func DaemonIDPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			dir = filepath.Join(".", ".config")
		} else {
			dir = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(dir, "human", "daemon-id")
}

// LoadOrCreateDaemonID reads the persisted id, or generates and stores one.
// An explicit override (config/flag) is honored by the caller and never
// persisted here.
func LoadOrCreateDaemonID() (string, error) {
	return loadOrCreateDaemonIDAt(DaemonIDPath())
}

func loadOrCreateDaemonIDAt(path string) (string, error) {
	data, err := afero.ReadFile(fs, path)
	if err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
		// An empty/whitespace file is a corrupted provisioning; regenerate
		// rather than stamp every marker with a blank id.
	} else if !os.IsNotExist(err) {
		// A read error other than "not found" (permission denied, NFS stall)
		// must propagate so we never replace an id the user cannot read.
		return "", errors.WrapWithDetails(err, "reading daemon id", "path", path)
	}

	id, err := GenerateDaemonID()
	if err != nil {
		return "", err
	}

	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := afero.WriteFile(fs, path, []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}
