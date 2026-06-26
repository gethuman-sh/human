package monarch

import (
	"os"
	"path/filepath"
)

// DefaultDBPath returns the path to the monarch database (~/.human/monarch.db),
// creating the directory if needed.
func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "monarch.db")
	}
	dir := filepath.Join(home, ".human")
	_ = os.MkdirAll(dir, 0o750)
	return filepath.Join(dir, "monarch.db")
}
