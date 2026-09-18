package cmddaemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"
)

// hostClaudeStore names the host's own Claude login — the store the
// description editor's headless turns run on — in claudeAuthRefusals. It is
// not a path because on macOS the store is a keychain item, not a file.
const hostClaudeStore = "host"

// claudeAuthRefusals remembers, per Claude credential store, that a run on it
// was refused authentication. It exists because the store's contents cannot
// tell a live refresh token from one the auth server has already rejected:
// both are an expired access token beside a present refresh token, the normal
// resting state between runs (SC-912). Only a run that actually tried to use
// the store knows, so the doctor asks this record instead of guessing (SC-5036).
//
// A refusal is tied to the store's modification stamp at the moment it was
// observed. Any rewrite of the store — a fresh /login, or a refresh that
// finally succeeded — moves the stamp and retires the evidence, so the doctor
// clears on the remedy itself rather than waiting for the next run to prove it.
type claudeAuthRefusals struct {
	mu      sync.Mutex
	byStore map[string]time.Time
}

func newClaudeAuthRefusals() *claudeAuthRefusals {
	return &claudeAuthRefusals{byStore: map[string]time.Time{}}
}

// record notes that a run on store was refused while the store carried stamp.
// Nil-safe so a daemon wired without the record simply stops remembering.
func (r *claudeAuthRefusals) record(store string, stamp time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byStore[store] = stamp
}

// clear forgets a refusal once a run on the store has succeeded.
func (r *claudeAuthRefusals) clear(store string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byStore, store)
}

// refused reports whether the last run on store was refused and the store has
// not been rewritten since. An unreadable stamp is the zero time on both
// sides, so the evidence then holds until a run succeeds — positive evidence of
// a dead login outranks a stamp the probe could not read.
func (r *claudeAuthRefusals) refused(store string, stamp time.Time) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	recorded, ok := r.byStore[store]
	if !ok {
		return false
	}
	if !recorded.Equal(stamp) {
		delete(r.byStore, store)
		return false
	}
	return true
}

// containerClaudeStore is the host copy of a project's in-container Claude
// store: <dir>/.devcontainer/claude/ is bind-mounted to ~/.claude in the
// container, so this file is the one every board agent of the project uses.
func containerClaudeStore(dir string) string {
	return filepath.Join(dir, ".devcontainer", "claude", ".credentials.json")
}

// fileStamp is a file store's modification time, zero when unreadable.
func fileStamp(path string) time.Time {
	info, err := os.Stat(path) // #nosec G703 -- path is a Claude store path built from the daemon's own project registry or the host config dir, not external input
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// keychainMdatRe reads the item's modification date from the attribute dump
// `security find-generic-password` prints without -w, e.g.
// "mdat"<timedate>=0x3230…  "20260914085800Z\000".
var keychainMdatRe = regexp.MustCompile(`"mdat"<timedate>=0x[0-9A-Fa-f]+\s+"(\d{14})Z`)

// hostClaudeStoreStamp is the modification stamp of the host's Claude login.
// On macOS it is the keychain item's mdat, read from its attributes only —
// never the secret, so the probe raises no keychain approval prompt. Elsewhere
// Claude Code keeps the login in .credentials.json under its config dir.
// CLAUDE_CONFIG_DIR renames the macOS keychain item to a name this probe does
// not derive, so there it reports zero and the evidence waits for a success.
func hostClaudeStoreStamp(ctx context.Context) time.Time {
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if runtime.GOOS == "darwin" {
		if configDir != "" {
			return time.Time{}
		}
		out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials").Output() // #nosec G204 -- fixed binary and arguments
		if err != nil {
			return time.Time{}
		}
		return parseKeychainMdat(string(out))
	}
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return time.Time{}
		}
		configDir = filepath.Join(home, ".claude")
	}
	return fileStamp(filepath.Join(configDir, ".credentials.json"))
}

func parseKeychainMdat(attrs string) time.Time {
	m := keychainMdatRe.FindStringSubmatch(attrs)
	if m == nil {
		return time.Time{}
	}
	t, err := time.Parse("20060102150405", m[1])
	if err != nil {
		return time.Time{}
	}
	return t
}
