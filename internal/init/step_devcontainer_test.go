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
			cfg := buildDevcontainerConfig(tt.proxy, tt.intercept, nil, tt.caPresent, "")
			got := mountsContainCACert(cfg.Mounts)
			if got != tt.wantMount {
				t.Errorf("mountsContainCACert = %v, want %v (mounts: %v)", got, tt.wantMount, cfg.Mounts)
			}
		})
	}
}

// The daemon hands containers it starts an address already resolved to an IP,
// so no generated variant may overwrite it: reading /etc/hosts is the fallback
// for the standalone `devcontainer up` path only. The fallback itself stays
// pinned to the first line — a host mapped twice makes getent print two, and
// the address would carry a newline, which iptables rejects under set -e,
// taking the rest of the && chain (CA trust, agent install, chrome bridge)
// with it.
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
			cfg := buildDevcontainerConfig(tt.proxy, tt.intercept, nil, tt.caPresent, "")
			cmd := cfg.PostStartCommand
			if !strings.Contains(cmd, "getent hosts host.docker.internal") {
				t.Fatalf("postStartCommand does not read the proxy host: %s", cmd)
			}
			if !strings.Contains(cmd, "awk 'NR==1{print $1}'") {
				t.Errorf("postStartCommand must take only the first hosts line: %s", cmd)
			}
			if !strings.Contains(cmd, `case "$HUMAN_PROXY_ADDR" in`) {
				t.Errorf("postStartCommand must leave an already-resolved address alone: %s", cmd)
			}
		})
	}
}

// goStack returns the Go stack from the shipped registry, so the test cannot
// pass against a registry that no longer carries it.
func goStack(t *testing.T) StackType {
	t.Helper()
	for _, s := range StackRegistry() {
		if s.FeatureKey == goFeatureKey {
			return s
		}
	}
	t.Fatalf("StackRegistry has no Go stack")
	return StackType{}
}

// An unpinned go feature floats free of go.mod; the image the wizard writes
// must ship exactly what go.mod requires or a later go.mod bump silently
// outruns it (SC-5879).
func TestBuildDevcontainerConfig_PinsGoFeatureFromGoMod(t *testing.T) {
	cfg := buildDevcontainerConfig(false, false, []StackType{goStack(t)}, false, "1.26.6")
	opts, ok := cfg.Features[goFeatureKey].(map[string]any)
	if !ok {
		t.Fatalf("Go feature options missing or wrong type: %#v", cfg.Features[goFeatureKey])
	}
	if got := len(opts); got != 1 {
		t.Fatalf("Go feature options = %#v, want exactly {version: 1.26.6}", opts)
	}
	if opts["version"] != "1.26.6" {
		t.Errorf("Go feature version = %v, want 1.26.6", opts["version"])
	}
}

// With no go.mod to read, the feature stays unpinned rather than pinned to an
// empty string — an explicit "" pin would be a worse devcontainer.json than no
// pin at all.
func TestBuildDevcontainerConfig_LeavesGoFeatureUnpinnedWithoutGoMod(t *testing.T) {
	cfg := buildDevcontainerConfig(false, false, []StackType{goStack(t)}, false, "")
	opts, ok := cfg.Features[goFeatureKey].(map[string]any)
	if !ok {
		t.Fatalf("Go feature options missing or wrong type: %#v", cfg.Features[goFeatureKey])
	}
	if _, has := opts["version"]; has {
		t.Errorf("Go feature must carry no version key without go.mod, got %#v", opts)
	}
}

// The toolchain check must be the LAST link in the chain: earlier, a Go
// version mismatch would short-circuit the proxy redirect and CA trust and
// present as a certificate failure at the model API instead (SC-4819's
// anti-pattern, SC-5879).
func TestBuildDevcontainerConfig_ChecksTheToolchainLast(t *testing.T) {
	cfg := buildDevcontainerConfig(true, true, []StackType{goStack(t)}, true, "1.26.6")
	cmd := cfg.PostStartCommand
	if !strings.HasSuffix(cmd, " && "+toolchainCheckCmd) {
		t.Fatalf("postStartCommand must end with the toolchain check, got: %s", cmd)
	}
	if idx := strings.Index(cmd, "human-proxy-setup"); idx < 0 || idx > strings.Index(cmd, toolchainCheckCmd) {
		t.Errorf("the proxy setup must precede the toolchain check, got: %s", cmd)
	}
}

// No Go stack selected, no toolchain check appended — the check answers a
// question that only applies to a Go container.
func TestBuildDevcontainerConfig_NoToolchainCheckWithoutGoStack(t *testing.T) {
	nodeOnly := []StackType{{Label: "Node.js", FeatureKey: nodeFeatureKey, Fixed: true}}
	cfg := buildDevcontainerConfig(false, false, nodeOnly, false, "")
	if strings.Contains(cfg.PostStartCommand, toolchainCheckCmd) {
		t.Errorf("postStartCommand must not check the toolchain without a Go stack: %s", cfg.PostStartCommand)
	}
}

// goModRequirement reads go.mod through the FileWriter, not the OS, so the
// devcontainer step stays testable without touching the real filesystem.
func TestGoModRequirement_readsFromFileWriter(t *testing.T) {
	fw := newMockFileWriter()
	if err := fw.WriteFile("go.mod", []byte("module x\n\ngo 1.26.6\n"), 0o644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	if got := goModRequirement(fw); got != "1.26.6" {
		t.Errorf("goModRequirement = %q, want 1.26.6", got)
	}
}

func TestGoModRequirement_noGoMod(t *testing.T) {
	fw := newMockFileWriter()
	if got := goModRequirement(fw); got != "" {
		t.Errorf("goModRequirement = %q, want empty without go.mod", got)
	}
}
