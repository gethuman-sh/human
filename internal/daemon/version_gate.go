package daemon

import "strings"

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
