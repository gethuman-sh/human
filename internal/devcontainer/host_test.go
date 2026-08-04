package devcontainer

import "testing"

// The container's redirect is installed with iptables, which refuses a hostname
// outright — so the address handed to a container has to be an IP wherever the
// host can determine one, and may only fall back to the name the container
// resolves itself when it cannot.
func TestProxyAddrForContainer(t *testing.T) {
	tests := []struct {
		name      string
		reachable string
		want      string
	}{
		{name: "bridge gateway is handed over resolved", reachable: "172.17.0.1", want: "172.17.0.1:19287"},
		{name: "loopback is not container-reachable, keep the name", reachable: LoopbackHost, want: "host.docker.internal:19287"},
		{name: "unknown host keeps the name", reachable: "", want: "host.docker.internal:19287"},
		{name: "ipv6 gateway is bracketed", reachable: "fd00::1", want: "[fd00::1]:19287"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proxyAddrForContainer(tt.reachable, 19287); got != tt.want {
				t.Errorf("proxyAddrForContainer(%q) = %q, want %q", tt.reachable, got, tt.want)
			}
		})
	}
}
