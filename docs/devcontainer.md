# Devcontainer / Remote mode

When running AI agents inside devcontainers, credentials should stay on the host. The daemon mode splits `human` into two roles:

- **Daemon** — runs on the host, holds credentials, executes all commands
- **Client** — runs inside the container, forwards CLI args to the daemon, prints results

## Mode detection

| Condition | Mode |
|-----------|------|
| `HUMAN_DAEMON_ADDR` not set | **Standalone** — normal CLI behavior |
| `HUMAN_DAEMON_ADDR` set (e.g. `localhost:19285`) | **Client** — forwards args to daemon |
| `human daemon start` subcommand | **Daemon** — listens for requests |

## Commands

```bash
human daemon start [--addr=:19285]   # start listening, print token, block until Ctrl-C
human daemon token                    # print current token (generate if needed)
human daemon status [--addr=...]      # check if daemon is reachable
```

## Authentication

A 32-byte random hex token is generated on first run of `human daemon start` and stored at `~/.config/human/daemon-token` (mode 0600). Every request from the client must include this token; the daemon rejects mismatches.

## Environment variables

| Variable | Description |
|----------|-------------|
| `HUMAN_DAEMON_ADDR` | Daemon address (e.g. `localhost:19285`). When set, `human` runs in client mode. |
| `HUMAN_DAEMON_TOKEN` | Shared secret for authenticating with the daemon. |

## Devcontainer setup

1. Start the daemon on the host:
   ```bash
   human daemon start
   ```

2. Configure `devcontainer.json`:
   ```json
   {
     "forwardPorts": [19285],
     "remoteEnv": {
       "HUMAN_DAEMON_ADDR": "host.docker.internal:19285",
       "HUMAN_DAEMON_TOKEN": "<paste from 'human daemon token'>"
     }
   }
   ```

3. Inside the container, all commands work transparently:
   ```bash
   human jira issues list --project=KAN
   human notion search "quarterly report"
   human figma file get ABC123
   ```

When `HUMAN_DAEMON_ADDR` is not set, `human` runs in standalone mode — no daemon required.

## HTTPS proxy

The daemon runs a transparent HTTPS proxy on port 19287 that filters outbound traffic from devcontainers using SNI-based domain matching. No certificates or traffic decryption needed.

### Configuration

Add to `.humanconfig.yaml` on the host:

```yaml
proxy:
  mode: allowlist    # or "blocklist"
  domains:
    - "*.github.com"
    - "github.com"
    - "api.openai.com"
    - "registry.npmjs.org"
    - "proxy.golang.org"
    - "sum.golang.org"
```

| Mode | Behavior |
|------|----------|
| `allowlist` | Only listed domains pass, everything else blocked |
| `blocklist` | Only listed domains blocked, everything else passes |
| No `proxy:` section | Block all (safe default) |

Wildcard `*.example.com` matches subdomains but not `example.com` itself.

The policy is read from the **registered project directory** (`--project`, or the
working directory when the daemon is started without one) — never from the
daemon's own working directory, which is `/` when the desktop app launches it.
Register several projects and no policy is chosen at all: one project's
allowlist must not widen another's egress, so the daemon keeps blocking and
prints why. Run one daemon per project.

A block-all policy announces itself on the startup banner and in the daemon log
(`proxy: blocking all egress — <reason>`), and the `egress` doctor check goes
red when the effective policy blocks the model API for a project whose
containers are redirected through the proxy. That check is launch-critical: the
daemon starts no agents while it fails, because a container that cannot reach
the model API only burns a stage's retry budget to rediscover it.

### Environment variables

| Variable | Description |
|----------|-------------|
| `HUMAN_PROXY_ADDR` | Proxy address. Defaults to `host.docker.internal:19287` in generated configs. |

### Devcontainer setup

Enable the `proxy` option in the [treehouse](https://github.com/gethuman-sh/treehouse) devcontainer Feature:

```json
{
  "features": {
    "ghcr.io/gethuman-sh/treehouse/human:1": {
      "proxy": true
    }
  },
  "capAdd": ["NET_ADMIN"],
  "remoteEnv": {
    "HUMAN_PROXY_ADDR": "host.docker.internal:19287"
  },
  "postStartCommand": "sudo human-proxy-setup"
}
```

The generated config uses `host.docker.internal:19287` by default — Docker's built-in DNS name that resolves to the host machine. No manual env var export needed.

The `proxy: true` option installs `iptables` and a setup script at image build time. At container start, `human-proxy-setup` reads `HUMAN_PROXY_ADDR` and redirects outbound HTTPS traffic to the proxy. If the variable is unset, the script skips gracefully.

### Go toolchain

The Go devcontainer feature is pinned to the version `go.mod` requires — the
wizard writes the pin from `go.mod`, and `TestDevcontainerPinsTheGoVersionGoModRequires`
fails `make check` if the two drift. Unpinned, the feature floats: an image
resolved before a `go.mod` bump ships an older Go, every `go` invocation then
fails at toolchain selection, and `GOTOOLCHAIN=local` refuses the module
outright (SC-5879).

The container's bootstrap runs `human doctor toolchain` right after
`human chrome-bridge` — after the proxy redirect and CA trust (so a mismatch
never presents as a certificate failure), and before any LSP install link
(`go install`, `npm install -g`, …). It compares `go.mod` against the
installed toolchain and fails naming both versions. It runs before, not after,
the LSP installs because a Go stack's own install link is `go install
golang.org/x/tools/gopls@latest` — broken by the very mismatch the check
exists to report — and the shell's `&&` chaining would otherwise let that
earlier failure short-circuit the check before it ever runs. Note that a
failing `postStartCommand` is reported as a warning and does not abort the
container (`internal/devcontainer/hooks.go`), so the pin and the `make check`
gate are what prevent the mismatch; the check is what says so if it happens
anyway.

Go's automatic toolchain switch — the recovery when the image's Go is older than
`go.mod` — downloads from `proxy.golang.org` and verifies against
`sum.golang.org`. Both must be in the allowlist, and `human init` now writes them
for any project with a Go stack. An **existing** project's allowlist lives in its
own host-side `.humanconfig.yaml` and is not regenerated: add the two hosts under
`proxy.domains` there and restart the daemon. Until then a blocked fetch reaches
the ticket the same way any policy denial does — the daemon reds the card naming
the host and the exact line to add (SC-5840).
