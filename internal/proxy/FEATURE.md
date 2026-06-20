# HTTPS Proxy

A transparent HTTPS proxy that lets `human` filter the outbound network traffic of AI agents running in devcontainers. It decides which hosts an agent may reach and records what it tried to access, so an agent can only talk to the destinations you allow.

- Filters outbound traffic by domain allow/blocklist
- Reads TLS SNI without decrypting traffic
- Optionally inspects HTTPS via a trusted local CA
- Prompts interactively to approve unknown hosts
- Records per-host connection statistics
- Forwards approved connections transparently upstream
- Blocks everything by default when unconfigured
