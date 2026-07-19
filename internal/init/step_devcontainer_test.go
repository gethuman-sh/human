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
