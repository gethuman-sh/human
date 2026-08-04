package init

import (
	"strings"
	"testing"
)

// mountsContainCACert reports whether any bind mount targets the human CA cert.
func mountsContainCACert(mounts []string) bool {
	for _, m := range mounts {
		if strings.Contains(m, "/.human/ca.crt") {
			return true
		}
	}
	return false
}

// The ca.crt mount only has meaning when MITM intercept is on: the trust
// wiring that consumes it lives inside case proxy && intercept. Emitting the
// mount for no-proxy or proxy-without-intercept confused users with a stray
// certificate. Guard: mount present iff proxy && intercept && caPresent.
func TestBuildDevcontainerConfig_CACertMountGating(t *testing.T) {
	tests := []struct {
		name      string
		proxy     bool
		intercept bool
		caPresent bool
		wantMount bool
	}{
		{name: "no proxy, no intercept, ca present", proxy: false, intercept: false, caPresent: true, wantMount: false},
		{name: "proxy, no intercept, ca present", proxy: true, intercept: false, caPresent: true, wantMount: false},
		{name: "proxy and intercept, ca present", proxy: true, intercept: true, caPresent: true, wantMount: true},
		{name: "proxy and intercept, ca absent", proxy: true, intercept: true, caPresent: false, wantMount: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := buildDevcontainerConfig(tt.proxy, tt.intercept, nil, tt.caPresent)
			got := mountsContainCACert(cfg.Mounts)
			if got != tt.wantMount {
				t.Errorf("mountsContainCACert = %v, want %v (mounts: %v)", got, tt.wantMount, cfg.Mounts)
			}
		})
	}
}

// Every generated proxy variant reads the proxy host out of /etc/hosts. A host
// mapped twice makes getent print two lines, and an unqualified awk turns that
// into a HUMAN_PROXY_ADDR carrying a newline — human-proxy-setup then feeds it
// to iptables, dies under set -e, and takes the rest of the && chain (CA trust,
// agent install, chrome bridge) with it. NR==1 is what keeps a duplicate host
// entry from silently disarming the container's proxy redirect.
func TestBuildDevcontainerConfig_ProxyAddrTakesFirstHostsLine(t *testing.T) {
	tests := []struct {
		name      string
		proxy     bool
		intercept bool
		caPresent bool
	}{
		{name: "proxy and intercept with ca", proxy: true, intercept: true, caPresent: true},
		{name: "proxy and intercept without ca", proxy: true, intercept: true, caPresent: false},
		{name: "proxy only", proxy: true, intercept: false, caPresent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := buildDevcontainerConfig(tt.proxy, tt.intercept, nil, tt.caPresent)
			cmd := cfg.PostStartCommand
			if !strings.Contains(cmd, "getent hosts host.docker.internal") {
				t.Fatalf("postStartCommand does not read the proxy host: %s", cmd)
			}
			if !strings.Contains(cmd, "awk 'NR==1{print $1}'") {
				t.Errorf("postStartCommand must take only the first hosts line: %s", cmd)
			}
		})
	}
}
