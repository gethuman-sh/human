# Documentation

## Configuration

`.humanconfig.yaml` holds named tracker instances. Multiple instances per tracker are supported. By default the first entry is used; select a specific one with `--tracker`:

```yaml
jiras:
  - name: work
    url: https://work.atlassian.net
    user: me@work.com
    key: api-token

githubs:
  - name: personal
    token: ghp_xxx

gitlabs:
  - name: work
    token: glpat-xxx

linears:
  - name: work
    token: lin_xxx

azuredevops:
  - name: work
    org: myorg
    token: pat-xxx

shortcuts:
  - name: work
    token: xxx

notions:
  - name: work
    token: ntn_xxx

figmas:
  - name: design
    token: figd_xxx

amplitudes:
  - name: product
    url: https://analytics.eu.amplitude.com
    # key + secret from env
```

Select a specific instance with `--tracker`:

```bash
human --tracker=personal list --project=KAN
human --tracker=work list --project=octocat/hello-world
```

Quick commands auto-detect the tracker from key format and configuration:

```bash
human get KAN-1                  # auto-detects tracker from key format
human list --project=KAN         # auto-detects when one tracker type is configured
```

When only one tracker type is configured, it is auto-detected. When multiple tracker types are configured, specify which one with `--tracker=<name>`. Provider-specific commands (`human jira issues list ...`) also continue to work.

List available statuses for an issue and set the status:

```bash
human jira issue statuses KAN-1              # JSON output
human jira issue statuses KAN-1 --table      # human-readable table
human jira issue status KAN-1 "Done"         # set issue status
```

Quick commands auto-detect the tracker:

```bash
human statuses KAN-1
human status KAN-1 "Done"
```

Edit an existing issue's title and/or description (both flags are optional, but at least one is required):

```bash
human jira issue edit KAN-1 --title "New title"
human jira issue edit KAN-1 --description "Updated description"
human github issue edit octocat/repo#42 --title "New title" --description "Updated desc"
```

List all configured trackers (JSON output, also the default when run without arguments):

```bash
human tracker list
```

### Settings resolution

Each setting is resolved in priority order (highest wins):

1. **Global env vars** (e.g. `JIRA_URL`)
2. **Per-instance env vars** (e.g. `JIRA_WORK_URL` — name uppercased)
3. **`.humanconfig.yaml`** — selected entry fills remaining gaps

There are no per-tracker credential flags. Credentials arrive through the env
convention above or `.humanconfig.yaml` (which can hold `1pw://` and `gh://`
vault references) — one mechanism that works for every tracker, so adding a
tracker never means adding flags. Use `--tracker <name>` to pick between
configured instances.

| Tracker | Env prefix | Settings | Default URL |
|---------|-----------|----------|-------------|
| Jira | `JIRA_` | `URL`, `USER`, `KEY` | — |
| GitHub | `GITHUB_` | `URL`, `TOKEN` | `https://api.github.com` |
| GitLab | `GITLAB_` | `URL`, `TOKEN` | `https://gitlab.com` |
| Linear | `LINEAR_` | `URL`, `TOKEN` | `https://api.linear.app` |
| Azure DevOps | `AZURE_` | `URL`, `ORG`, `TOKEN` | `https://dev.azure.com` |
| Shortcut | `SHORTCUT_` | `URL`, `TOKEN` | `https://api.app.shortcut.com` |
| Notion | `NOTION_` | `URL`, `TOKEN` | `https://api.notion.com` |
| Figma | `FIGMA_` | `URL`, `TOKEN` | `https://api.figma.com` |
| Amplitude | `AMPLITUDE_` | `URL`, `KEY`, `SECRET` | `https://amplitude.com` |

## Daemon mode (devcontainers)

When running AI agents inside devcontainers, credentials should stay on the host. The daemon mode splits `human` into two roles:

- **Daemon** — runs on the host, holds credentials, executes all commands
- **Client** — runs inside the container, forwards CLI args to the daemon, prints results

### Mode detection

| Condition | Mode |
|-----------|------|
| `HUMAN_DAEMON_ADDR` not set | **Standalone** — normal CLI behavior |
| `HUMAN_DAEMON_ADDR` set (e.g. `localhost:19285`) | **Client** — forwards args to daemon |
| `human daemon start` subcommand | **Daemon** — listens for requests |

### Commands

```bash
human daemon start [--addr=:19285]   # start listening, print token, block until Ctrl-C
human daemon token                    # print current token (generate if needed)
human daemon status [--addr=...]      # check if daemon is reachable
```

### Authentication

A 32-byte random hex token is generated on first run of `human daemon start` and stored at `~/.config/human/daemon-token` (mode 0600). Every request from the client must include this token; the daemon rejects mismatches.

### Environment variables

| Variable | Description |
|----------|-------------|
| `HUMAN_DAEMON_ADDR` | Daemon address (e.g. `localhost:19285`). When set, `human` runs in client mode. |
| `HUMAN_DAEMON_TOKEN` | Shared secret for authenticating with the daemon. |

### Devcontainer setup

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
   human get KAN-1
   human list --project=KAN
   human jira issues list --project=KAN
   human notion search "quarterly report"
   human figma file get ABC123
   ```
