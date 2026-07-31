//go:build linux

package daemon

import (
	"golang.org/x/sys/unix"

	"github.com/gethuman-sh/human/errors"
)

// hardenProcess asks the kernel to stop handing this process's memory out.
//
// The daemon holds every tracker credential on the machine in plaintext, for as
// long as it runs. Two ordinary, non-adversarial events publish that: a crash
// writes a core file containing every secret (into a directory that collects
// crash reports, quite possibly attached to a bug report later), and any process
// running as the same user can attach and read the memory directly.
//
// PR_SET_DUMPABLE covers both — no core is written, and ptrace by a non-root
// process is refused — which is why it comes first. RLIMIT_CORE follows it
// because a collector like systemd-coredump can be configured to produce a dump
// through paths the dumpable flag alone does not settle.
//
// This is deliberately NOT protection against root: anything with
// CAP_SYS_PTRACE still reads the process, and on this machine that is already
// decisive, since the daemon hands credentials to whoever reaches its socket
// with the token. It closes the accidental disclosures.
func hardenProcess() ([]string, error) {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return nil, errors.WrapWithDetails(err, "marking the daemon process non-dumpable", "call", "PR_SET_DUMPABLE")
	}
	applied := []string{"no core dump", "no ptrace by other processes"}
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		// The dumpable flag already carries the protection; a refused rlimit is
		// worth reporting but not worth refusing to start over.
		return applied, errors.WrapWithDetails(err, "disabling core dumps", "call", "RLIMIT_CORE")
	}
	return append(applied, "core size limit 0"), nil
}
