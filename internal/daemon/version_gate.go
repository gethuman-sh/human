package daemon

import (
	"fmt"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// MinClientVersion is the oldest client wire protocol this daemon accepts.
// Bump it whenever the protocol changes incompatibly, so stale clients get
// one clear "upgrade" error instead of failing mid-handshake with side
// effects already applied (last incompatible change: the HUM-160
// permission-grant cycle, shipping in 0.21.0).
const MinClientVersion = "0.21.0"

// clientVersionSupported reports whether a client's self-reported version may
// talk to this daemon. Dev builds pass — they are built from the same tree as
// the daemon they test against. Empty or unparseable versions are rejected: a
// client that cannot state its version predates the handshake itself.
func clientVersionSupported(version string) bool {
	v := strings.TrimSpace(version)
	if v == "dev" || strings.HasPrefix(v, "dev ") {
		return true
	}
	got, ok := parseSemver(v)
	if !ok {
		return false
	}
	want, _ := parseSemver(MinClientVersion)
	return !semverLess(got, want)
}

// parseSemver reads up to major.minor.patch from a version string, tolerating
// a leading "v" and trailing non-numeric suffixes ("0.21.0-rc1", "0.21.0
// (abc123) 2026-…"). Missing parts default to zero.
func parseSemver(s string) ([3]int, bool) {
	s = strings.TrimPrefix(s, "v")
	if cut := strings.IndexAny(s, " -+("); cut >= 0 {
		s = s[:cut]
	}
	var out [3]int
	parts := strings.SplitN(s, ".", 3)
	for i, p := range parts {
		n, ok := leadingInt(p)
		if !ok {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, len(parts) > 0 && parts[0] != ""
}

func leadingInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			if i == 0 {
				return 0, false
			}
			break
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func semverLess(a, b [3]int) bool {
	for i := range 3 {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// Protocol is the wire protocol this build speaks. Bump it on EVERY change to
// the daemon↔client wire — new routes, new request fields, changed semantics —
// additive or breaking alike. MinProtocol moves only for breaking changes.
// Every bump gets a line in docs/protocol.md so the decision is auditable.
const Protocol = 1

// MinProtocol is the oldest client protocol this daemon still serves. Raising
// it is the CONSCIOUS compatibility decision: the author of a breaking wire
// change bumps it in the same commit and answers "which clients am I cutting
// off" in docs/protocol.md. Additive changes leave it alone, so a daemon at
// protocol 10 keeps serving a client at 8.
const MinProtocol = 1

// MinDaemonProtocol is the oldest daemon protocol this client accepts. It is
// the symmetric half of the gate: without it, a newer client on an older
// daemon fails with a bare "unknown command" instead of one clear
// rebuild-the-daemon error. It rises only when the client depends on daemon
// behavior older daemons lack.
const MinDaemonProtocol = 1

// clientSupported reports whether a client may talk to this daemon. Clients
// that advertise a protocol get the integer gate (>= MinProtocol — newer
// clients pass, their own MinDaemonProtocol guards the other direction).
// Protocol-less clients predate the handshake and fall back to the legacy
// version-string gate.
func clientSupported(version string, protocol int) bool {
	if protocol > 0 {
		return protocol >= MinProtocol
	}
	return clientVersionSupported(version)
}

// DaemonProtocolError returns a non-nil error when the daemon's advertised
// protocol is too old for this client to use. Daemons that predate protocol
// advertising (Protocol 0 in daemon.json) pass — the version-skew warning
// covers them, and refusing them would strand every client during the
// transition.
func DaemonProtocolError(info DaemonInfo) error {
	if info.Protocol > 0 && info.Protocol < MinDaemonProtocol {
		return errors.WithDetails(fmt.Sprintf(
			"daemon speaks protocol %d but this client needs >= %d — rebuild and restart the daemon (make build && human daemon restart)",
			info.Protocol, MinDaemonProtocol),
			"daemon_protocol", info.Protocol, "client_min", MinDaemonProtocol)
	}
	return nil
}
