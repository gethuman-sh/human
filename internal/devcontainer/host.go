package devcontainer

import (
	"context"
	"net"
	"runtime"
	"strconv"
	"time"

	"github.com/moby/moby/client"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/dockerhost"
)

// LoopbackHost is the loopback address the daemon binds when containers reach it
// via loopback (Docker Desktop forwards host.docker.internal to the host
// loopback) or when Docker is unavailable.
const LoopbackHost = "127.0.0.1"

// ContainerReachableHost returns the host address the human daemon should listen
// on so that agents running inside Docker containers can reach it — WITHOUT
// binding 0.0.0.0, which would expose the credential-holding daemon to the LAN.
//
//   - Docker Desktop (macOS/Windows): loopback. The Desktop VM forwards
//     host.docker.internal to the host loopback, so 127.0.0.1 is reachable from
//     containers and never needs widening.
//   - Linux native Docker: the docker bridge gateway (e.g. 172.17.0.1).
//     host.docker.internal:host-gateway resolves to it, and it is a host-only
//     interface, not LAN-routable. Falls back to loopback when the gateway
//     can't be determined.
//   - Docker unavailable: loopback (no container will run until Docker is up).
//
// Note the trade-off on Linux: the bridge gateway lives on docker0, so if the
// Docker daemon stops, that interface disappears and the daemon must be
// restarted to rebind. That is acceptable for a container-first workflow.
func ContainerReachableHost() string {
	// Only native Linux Docker exposes a host-side bridge whose gateway the
	// daemon must bind; on Docker Desktop the loopback is already reachable.
	if runtime.GOOS != "linux" {
		return LoopbackHost
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if gw := dockerBridgeGateway(ctx); gw != "" {
		return gw
	}
	return LoopbackHost
}

// proxyAddrForContainer returns the proxy address in the form a container's
// redirect can actually use. iptables resolves nothing — nf_tables rejects a
// hostname in --to-destination outright — so the redirect needs a literal IP,
// and the container was left deriving one out of /etc/hosts. On Linux the
// daemon binds the proxy on the docker bridge gateway, which is precisely what
// host.docker.internal maps to inside the container, so hand it over already
// resolved and nothing in the container has to read /etc/hosts at all. Where
// the gateway is not knowable host-side (Docker Desktop, Docker down) the
// hostname stays and the container resolves it itself.
func proxyAddrForContainer(reachableHost string, port int) string {
	if reachableHost == "" || reachableHost == LoopbackHost {
		return net.JoinHostPort(daemon.DockerHost, strconv.Itoa(port))
	}
	return net.JoinHostPort(reachableHost, strconv.Itoa(port))
}

// dockerBridgeGateway returns the gateway IP of Docker's default bridge network,
// or "" if Docker is unreachable or the gateway is unknown.
func dockerBridgeGateway(ctx context.Context) string {
	cli, err := newRawDockerClient()
	if err != nil {
		return ""
	}
	defer func() { _ = cli.Close() }()
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		return "" // Docker not running
	}
	insp, err := cli.NetworkInspect(ctx, "bridge", client.NetworkInspectOptions{})
	if err != nil {
		return ""
	}
	for _, cfg := range insp.Network.IPAM.Config {
		if cfg.Gateway.IsValid() {
			return cfg.Gateway.String()
		}
	}
	return ""
}

// newRawDockerClient builds a raw Docker SDK client with the same host
// resolution as NewDockerClient, for the operations (ping, network inspect) not
// exposed by the DockerClient interface.
func newRawDockerClient() (*client.Client, error) {
	opts := []client.Opt{client.FromEnv}
	if host := dockerhost.Resolve().Host; host != "" {
		opts = append(opts, client.WithHost(host))
	}
	return client.New(opts...)
}
