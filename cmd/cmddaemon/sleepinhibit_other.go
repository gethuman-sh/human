//go:build !linux

package cmddaemon

import "github.com/gethuman-sh/human/errors"

// logindInhibitor is unavailable off Linux/systemd; Acquire always errors so
// the loop logs the "runs are exposed" warning rather than silently pretending
// to protect the run.
type logindInhibitor struct{}

func (logindInhibitor) Acquire(who, why string) (func() error, error) {
	return nil, errors.WithDetails("sleep inhibition is only supported on Linux/systemd")
}
