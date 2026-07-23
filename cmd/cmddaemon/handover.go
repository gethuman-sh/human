package cmddaemon

import (
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// Handover env vars carry the live-socket handoff across a self-restart re-exec.
// A re-exec'd child inherits the parent's three listening sockets as extra file
// descriptors so a rebuild never tears the listeners down (no client sees a
// refused connection). Both are unset on a normal start.
const (
	// envInheritListeners names the inherited listener fds, e.g.
	// "daemon:3,proxy:4,chrome:5". Set only by the handover parent.
	envInheritListeners = "_HUMAN_INHERIT_LISTENERS"
	// envHandoverReadyFD is the fd the child writes to once it is up, so the
	// parent knows the child is serving before it stops. Set only on handover.
	envHandoverReadyFD = "_HUMAN_HANDOVER_READY_FD"
)

// listenerSet is the daemon's three listening sockets, owned by
// runDaemonForeground so the handover coordinator can pass them to a re-exec'd
// child. On a fresh start they are bound; on a handover child they are rebuilt
// from the inherited fds.
type listenerSet struct {
	daemon net.Listener
	proxy  net.Listener
	chrome net.Listener
}

// openListeners returns the daemon/proxy/chrome listeners. When the process is
// a handover child (envInheritListeners set) it adopts the inherited sockets;
// otherwise it binds the three addresses fresh. A partial bind failure closes
// what already opened so no socket leaks.
func openListeners(daemonAddr, proxyAddr, chromeAddr string) (*listenerSet, error) {
	if spec := os.Getenv(envInheritListeners); spec != "" {
		return inheritListeners(spec)
	}

	ls := &listenerSet{}
	var err error
	if ls.daemon, err = net.Listen("tcp", daemonAddr); err != nil {
		return nil, errors.WrapWithDetails(err, "binding daemon listener", "addr", daemonAddr)
	}
	if ls.proxy, err = net.Listen("tcp", proxyAddr); err != nil {
		_ = ls.daemon.Close()
		return nil, errors.WrapWithDetails(err, "binding proxy listener", "addr", proxyAddr)
	}
	if ls.chrome, err = net.Listen("tcp", chromeAddr); err != nil {
		_ = ls.daemon.Close()
		_ = ls.proxy.Close()
		return nil, errors.WrapWithDetails(err, "binding chrome listener", "addr", chromeAddr)
	}
	return ls, nil
}

// inheritListeners rebuilds the three listeners from the fds a handover parent
// passed as ExtraFiles, keyed by the "name:fd" spec so the mapping survives even
// if the fd ordering ever changes.
func inheritListeners(spec string) (*listenerSet, error) {
	fds := map[string]int{}
	for _, part := range strings.Split(spec, ",") {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			return nil, errors.WithDetails("malformed inherited-listener spec", "spec", spec)
		}
		fd, err := strconv.Atoi(kv[1])
		if err != nil {
			return nil, errors.WrapWithDetails(err, "parsing inherited fd", "part", part)
		}
		fds[kv[0]] = fd
	}

	ls := &listenerSet{}
	var err error
	if ls.daemon, err = listenerFromFD(fds["daemon"], "daemon"); err != nil {
		return nil, err
	}
	if ls.proxy, err = listenerFromFD(fds["proxy"], "proxy"); err != nil {
		_ = ls.daemon.Close()
		return nil, err
	}
	if ls.chrome, err = listenerFromFD(fds["chrome"], "chrome"); err != nil {
		_ = ls.daemon.Close()
		_ = ls.proxy.Close()
		return nil, err
	}
	return ls, nil
}

// listenerFromFD adopts one inherited fd as a net.Listener. net.FileListener
// dups the fd, so the temporary *os.File wrapper is closed immediately.
func listenerFromFD(fd int, name string) (net.Listener, error) {
	if fd <= 0 {
		return nil, errors.WithDetails("missing inherited listener fd", "name", name)
	}
	f := os.NewFile(uintptr(fd), "listener-"+name) // #nosec G115 -- fd is small and validated > 0
	ln, err := net.FileListener(f)
	_ = f.Close()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "adopting inherited listener", "name", name)
	}
	return ln, nil
}

// files duplicates each listener into an *os.File in a fixed order
// (daemon, proxy, chrome) so it can be handed to a re-exec'd child as
// ExtraFiles. handoverListenerSpec must describe the same order. On any error
// the already-duplicated files are closed.
func (ls *listenerSet) files() ([]*os.File, error) {
	ordered := []struct {
		name string
		ln   net.Listener
	}{
		{"daemon", ls.daemon},
		{"proxy", ls.proxy},
		{"chrome", ls.chrome},
	}
	var files []*os.File
	for _, entry := range ordered {
		tl, ok := entry.ln.(*net.TCPListener)
		if !ok {
			closeAllFiles(files)
			return nil, errors.WithDetails("listener is not a TCP listener", "name", entry.name)
		}
		f, err := tl.File()
		if err != nil {
			closeAllFiles(files)
			return nil, errors.WrapWithDetails(err, "duplicating listener fd", "name", entry.name)
		}
		files = append(files, f)
	}
	return files, nil
}

// handoverListenerSpec is the fixed name→fd mapping for the files() order. The
// child receives ExtraFiles starting at fd 3, so daemon=3, proxy=4, chrome=5.
const handoverListenerSpec = "daemon:3,proxy:4,chrome:5"

func closeAllFiles(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// signalHandoverReady tells a handover parent this child is up and serving, by
// writing to the fd named in envHandoverReadyFD. It is a no-op on a normal
// start (env unset), so runDaemonForeground can call it unconditionally.
func signalHandoverReady(logger zerolog.Logger) {
	v := os.Getenv(envHandoverReadyFD)
	if v == "" {
		return
	}
	fd, err := strconv.Atoi(v)
	if err != nil {
		logger.Warn().Str("value", v).Msg("invalid handover ready fd, parent will time out")
		return
	}
	f := os.NewFile(uintptr(fd), "handover-ready") // #nosec G115 -- fd is from our own parent, small
	if f == nil {
		return
	}
	_, _ = f.WriteString("ok\n")
	_ = f.Close()
}

// watchBinaryDisabled reports whether the self-restart watcher is switched off
// via HUMAN_DAEMON_WATCH_BINARY. Default is on: the failure mode is safe (the
// parent keeps serving if the child cannot boot), so a rebuild picks itself up
// without operator action.
func watchBinaryDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HUMAN_DAEMON_WATCH_BINARY"))) {
	case "0", "false", "off", "no":
		return true
	default:
		return false
	}
}
