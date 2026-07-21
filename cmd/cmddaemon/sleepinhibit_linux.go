//go:build linux

package cmddaemon

import (
	"os"

	"github.com/godbus/dbus/v5"

	"github.com/gethuman-sh/human/errors"
)

// logindInhibitor takes a logind "block" inhibitor for suspend/sleep. The
// returned unix FD embodies the block: logind holds it until the FD is closed,
// so release is a single Close.
type logindInhibitor struct{}

func (logindInhibitor) Acquire(who, why string) (func() error, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "connecting to system bus for sleep inhibitor")
	}
	// The inhibitor is returned as a unix file descriptor; without FD passing
	// negotiated on this transport the "h" return decodes to an index, not a
	// real fd, and Store would fail obscurely. Guard explicitly (library-
	// recommended) so a bus lacking FD passing surfaces the loud warning.
	if !conn.SupportsUnixFDs() {
		return nil, errors.WithDetails("system bus does not support unix fd passing; cannot hold sleep inhibitor")
	}
	obj := conn.Object("org.freedesktop.login1", dbus.ObjectPath("/org/freedesktop/login1"))
	// mode "block" defers suspend (vs "delay"); "sleep" covers suspend and
	// hibernate. The lock screen is unaffected — only the sleep transition is held.
	call := obj.Call("org.freedesktop.login1.Manager.Inhibit", 0, "sleep", who, why, "block")
	if call.Err != nil {
		return nil, errors.WrapWithDetails(call.Err, "acquiring logind sleep inhibitor")
	}
	var fd dbus.UnixFD
	if err := call.Store(&fd); err != nil {
		return nil, errors.WrapWithDetails(err, "reading logind inhibitor fd")
	}
	f := os.NewFile(uintptr(fd), "logind-sleep-inhibitor")
	if f == nil {
		return nil, errors.WithDetails("logind returned an invalid inhibitor fd")
	}
	// Close the FD only — never the shared SystemBus connection.
	return f.Close, nil
}
