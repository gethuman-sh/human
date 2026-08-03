package costledger

import (
	"os"
	"path/filepath"
)

// DefaultDBPath returns ~/.human/costledger.db, the durable per-ticket cost and
// time ledger, creating the directory if needed. Falls back to a relative path
// if the home dir cannot be resolved. Mirrors internal/stats/dbpath.go.
func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "costledger.db")
	}
	dir := filepath.Join(home, ".human")
	_ = os.MkdirAll(dir, 0o750)
	return filepath.Join(dir, "costledger.db")
}
