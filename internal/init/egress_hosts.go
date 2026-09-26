package init

import "strings"

// The hosts a container's own bootstrap must reach. The wizard generates BOTH
// the bootstrap commands and the allowlist, and nothing tied them together: the
// shipped default allowed neither Go module host while the same wizard wrote
// `go install …@latest` for every Go project, and Go's automatic toolchain
// switch — the recovery for an image whose Go is older than go.mod — needs the
// same two (SC-5879). One table now feeds the generated allowlist and the test
// that fails the build when a stack gains a command whose hosts nobody declared.
const (
	// goModuleProxyHost serves module and toolchain downloads; goChecksumDBHost
	// is what they are verified against. Blocking either makes EVERY go
	// invocation fail at toolchain selection when go.mod outruns the image.
	goModuleProxyHost = "proxy.golang.org"
	goChecksumDBHost  = "sum.golang.org"
	npmRegistryHost   = "registry.npmjs.org"
)

// bootstrapEgressHosts maps the package manager a generated command invokes to
// the hosts it fetches from. Keyed by the command's leading words because that
// is what the wizard actually writes.
var bootstrapEgressHosts = []struct {
	prefix string
	hosts  []string
}{
	{"go install", []string{goModuleProxyHost, goChecksumDBHost}},
	{"npm install", []string{npmRegistryHost}},
	{"gem install", []string{"rubygems.org", "index.rubygems.org"}},
	{"rustup", []string{"static.rust-lang.org"}},
}

// hostsForCommand returns the egress hosts an install command needs, nil when
// the command reaches nothing (or when no table entry claims it — which
// TestEveryBootstrapCommandDeclaresItsEgressHosts refuses to let ship).
func hostsForCommand(cmd string) []string {
	for _, entry := range bootstrapEgressHosts {
		if strings.HasPrefix(cmd, entry.prefix) {
			return entry.hosts
		}
	}
	return nil
}

// stackBootstrapCommands returns every install command the wizard may place in
// a container's bootstrap for these stacks: the devcontainer step's own
// (stackInstallCmd) and the LSP step's, which is seeded from the same stacks
// (StackToLspBinary -> LspRegistry) and appended to the same postStartCommand.
func stackBootstrapCommands(stacks []StackType) []string {
	installFor := make(map[string]string)
	for _, p := range LspRegistry() {
		installFor[p.Binary] = p.InstallCmd
	}
	toLsp := StackToLspBinary()

	var cmds []string
	for _, s := range stacks {
		if cmd := stackInstallCmd[s.FeatureKey]; cmd != "" {
			cmds = append(cmds, cmd)
		}
		if cmd := installFor[toLsp[s.FeatureKey]]; cmd != "" {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// StackEgressHosts returns every host the bootstrap of these stacks fetches
// from, plus the Go module proxy and checksum database whenever a Go stack is
// present: the toolchain switch needs them even with no `go install` written.
func StackEgressHosts(stacks []StackType) []string {
	var hosts []string
	seen := make(map[string]bool)
	add := func(h string) {
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	for _, cmd := range stackBootstrapCommands(stacks) {
		for _, h := range hostsForCommand(cmd) {
			add(h)
		}
	}
	for _, s := range stacks {
		if s.FeatureKey == goFeatureKey {
			add(goModuleProxyHost)
			add(goChecksumDBHost)
		}
	}
	return hosts
}

// ProxyDomainsForStacks is the allowlist the wizard writes: the defaults every
// container needs plus what the selected stacks' own bootstrap fetches from.
// Order is deterministic (defaults, then stack order) so a regenerated config
// produces the same bytes.
func ProxyDomainsForStacks(stacks []StackType) []string {
	domains := append([]string{}, DefaultProxyDomains...)
	seen := make(map[string]bool, len(domains))
	for _, d := range domains {
		seen[d] = true
	}
	for _, h := range StackEgressHosts(stacks) {
		if !seen[h] {
			seen[h] = true
			domains = append(domains, h)
		}
	}
	return domains
}
