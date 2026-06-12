package gui

import (
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/proxy"
)

// ProxyLogMode adapts the process-global proxy log mode to LogModeStore.
type ProxyLogMode struct{}

// Get returns the current traffic log mode as a string.
func (ProxyLogMode) Get() string { return proxy.LogModeString(proxy.GetLogMode()) }

// Set parses and applies a traffic log mode.
func (ProxyLogMode) Set(mode string) error {
	m, err := proxy.ParseLogMode(mode)
	if err != nil {
		return err
	}
	proxy.SetLogMode(m)
	return nil
}

// LoopbackRunner executes CLI commands through the daemon's own TCP
// endpoint. Routing GUI writes through the loopback (instead of calling
// providers directly) keeps destructive-op interception, env scoping,
// and audit on the single existing code path.
type LoopbackRunner struct {
	Addr  string
	Token string
}

// RunCapture runs args against the daemon and returns its stdout.
func (l LoopbackRunner) RunCapture(args []string) ([]byte, error) {
	return daemon.RunRemoteCapture(l.Addr, l.Token, args)
}
