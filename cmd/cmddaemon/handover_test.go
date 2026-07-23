//go:build !windows

package cmddaemon

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestOpenListenersFreshBindsThreeDistinctPorts(t *testing.T) {
	t.Setenv(envInheritListeners, "") // ensure fresh-bind path
	ls, err := openListeners("127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("openListeners: %v", err)
	}
	defer func() {
		_ = ls.daemon.Close()
		_ = ls.proxy.Close()
		_ = ls.chrome.Close()
	}()

	ports := map[string]bool{}
	for _, ln := range []net.Listener{ls.daemon, ls.proxy, ls.chrome} {
		if ln == nil {
			t.Fatal("nil listener")
		}
		ports[ln.Addr().String()] = true
	}
	if len(ports) != 3 {
		t.Fatalf("expected 3 distinct listener addresses, got %v", ports)
	}
}

func TestOpenListenersFreshFailsOnBadAddr(t *testing.T) {
	t.Setenv(envInheritListeners, "")
	if _, err := openListeners("127.0.0.1:0", "not-an-addr", "127.0.0.1:0"); err == nil {
		t.Fatal("expected error binding an invalid proxy address")
	}
}

// TestOpenListenersInheritRoundTrip binds three sockets, duplicates their fds
// the way a handover parent would, then adopts them back through the inherit
// path — proving the child rebuilds working listeners from inherited fds.
func TestOpenListenersInheritRoundTrip(t *testing.T) {
	parent, err := openListeners("127.0.0.1:0", "127.0.0.1:0", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind parent listeners: %v", err)
	}
	wantAddr := parent.daemon.Addr().String()

	files, err := parent.files()
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	defer closeAllFiles(files)
	// The parent keeps serving on its own copies; the child adopts the dups.
	defer func() {
		_ = parent.daemon.Close()
		_ = parent.proxy.Close()
		_ = parent.chrome.Close()
	}()

	spec := fmt.Sprintf("daemon:%d,proxy:%d,chrome:%d", files[0].Fd(), files[1].Fd(), files[2].Fd())
	t.Setenv(envInheritListeners, spec)

	child, err := openListeners("ignored", "ignored", "ignored")
	if err != nil {
		t.Fatalf("inherit listeners: %v", err)
	}
	defer func() {
		_ = child.daemon.Close()
		_ = child.proxy.Close()
		_ = child.chrome.Close()
	}()

	if got := child.daemon.Addr().String(); got != wantAddr {
		t.Fatalf("inherited daemon listener addr = %s, want %s (same socket)", got, wantAddr)
	}
}

func TestInheritListenersRejectsMalformedSpec(t *testing.T) {
	for _, spec := range []string{"daemon", "daemon:notanumber", "daemon:3,proxy", "proxy:4,chrome:5"} {
		if _, err := inheritListeners(spec); err == nil {
			t.Errorf("inheritListeners(%q) = nil error, want failure", spec)
		}
	}
}

func TestListenerFromFDRejectsInvalidFD(t *testing.T) {
	if _, err := listenerFromFD(0, "daemon"); err == nil {
		t.Error("listenerFromFD(0) = nil error, want failure")
	}
}

func TestSignalHandoverReadyWritesAndNoOps(t *testing.T) {
	// No-op when the env var is unset.
	_ = os.Unsetenv(envHandoverReadyFD)
	signalHandoverReady(zerolog.Nop()) // must not panic

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = r.Close() }()
	t.Setenv(envHandoverReadyFD, fmt.Sprintf("%d", w.Fd()))

	signalHandoverReady(zerolog.Nop())
	_ = w.Close()

	buf := make([]byte, 16)
	n, _ := r.Read(buf)
	if got := strings.TrimSpace(string(buf[:n])); got != "ok" {
		t.Fatalf("readiness fd got %q, want %q", got, "ok")
	}
}

func TestWatchBinaryDisabled(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"1":     false,
		"true":  false,
		"0":     true,
		"false": true,
		"OFF":   true,
		"no":    true,
	}
	for val, want := range cases {
		t.Setenv("HUMAN_DAEMON_WATCH_BINARY", val)
		if got := watchBinaryDisabled(); got != want {
			t.Errorf("watchBinaryDisabled() with %q = %v, want %v", val, got, want)
		}
	}
}
