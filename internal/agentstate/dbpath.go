package agentstate

import (
	"os"
	"path/filepath"
)

// DefaultDBPath returns the path to the agent state database
// (~/.human/state.db), creating the directory if needed. It sits beside
// confirms.db and index.db so all daemon-host state lives in one place.
//
// Because `human state` is forwarded to the daemon (it is deliberately absent
// from main.go's localSubcommands), this resolves on the daemon host — which is
// what makes the store shared across containers and agents rather than private
// to each caller.
func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "state.db")
	}
	dir := filepath.Join(home, ".human")
	_ = os.MkdirAll(dir, 0o750)
	return filepath.Join(dir, "state.db")
}
