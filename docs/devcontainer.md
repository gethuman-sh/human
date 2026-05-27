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
    - "api.openai.com"
    - "registry.npmjs.org"
```

| Mode | Behavior |
|------|----------|
| `allowlist` | Only listed domains pass, everything else blocked |
| `blocklist` | Only listed domains blocked, everything else passes |
| No `proxy:` section | Block all (safe default) |

Wildcard `*.example.com` matches subdomains but not `example.com` itself.

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
