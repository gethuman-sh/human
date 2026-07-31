//go:build darwin

package daemon

import (
	"golang.org/x/sys/unix"

	"github.com/gethuman-sh/human/errors"
)

// hardenProcess disables core dumps. macOS has no PR_SET_DUMPABLE equivalent,
// so the ptrace half of the Linux protection is simply unavailable here and is
// not claimed.
func hardenProcess() ([]string, error) {
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		return nil, errors.WrapWithDetails(err, "disabling core dumps", "call", "RLIMIT_CORE")
	}
	return []string{"core size limit 0"}, nil
}
