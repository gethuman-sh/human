# HTTPS Proxy

A transparent HTTPS proxy that lets `human` filter the outbound network traffic of AI agents running in devcontainers. It decides which hosts an agent may reach and records what it tried to access, so an agent can only talk to the destinations you allow.

- Filters outbound traffic by domain allow/blocklist
- Reads TLS SNI without decrypting traffic
- Optionally inspects HTTPS via a trusted local CA
- Prompts interactively to approve unknown hosts
- Records per-host connection statistics
- Records content-free model-call outcomes at the boundary — status class (ok, auth, rate-limit, overload, network, other) and timing, attributed to the ticket/stage that made the call, including calls that fail before any response exists; never any prompt or response body
- Forwards approved connections transparently upstream
- Blocks everything by default when unconfigured
