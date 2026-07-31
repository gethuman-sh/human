//go:build !linux && !darwin

package daemon

// hardenProcess does nothing on platforms without a process-wide way to refuse
// core dumps and debugger attachment. Reporting nothing applied is the point:
// HardenProcess logs what it got, so a platform with no protection says so
// rather than implying the daemon is hardened everywhere.
func hardenProcess() ([]string, error) {
	return nil, nil
}
