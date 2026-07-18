# Security Policy

## Supported Versions

Only the latest release of `human` receives security fixes. Please update to
the newest version before reporting an issue.

## Reporting a Vulnerability

**Do not open a public issue for security vulnerabilities.**

Report vulnerabilities privately via GitHub's private vulnerability reporting:

https://github.com/gethuman-sh/human/security/advisories/new

Alternatively, email stephan@amazingcto.com.

Please include:

- A description of the vulnerability and its impact
- Steps to reproduce (a proof of concept helps)
- The affected version (`human --version`) and platform

You will receive an acknowledgement within a few days. Once the issue is
confirmed, a fix is prepared and released before any details are published.

## Scope

`human` runs on developer machines and connects to issue trackers, code hosts,
and knowledge tools with the user's credentials. Of particular interest:

- Credential handling (vault references, token resolution, daemon storage)
- The daemon's local API surface
- The proxy allowlist and devcontainer isolation
- Injection via untrusted ticket, comment, or document content
