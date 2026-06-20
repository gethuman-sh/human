# Secret-Redacting Filesystem

A protective filesystem view that hides or blanks out your secrets, so an AI agent can work in your project without ever reading API keys, tokens, or private certificates.

- Hides secrets from agents reading files
- Redacts keys and tokens line by line
- Understands .env, JSON, and YAML files
- Blanks out private keys and certificates entirely
- Catches secrets hidden behind symlinks
- Lets ordinary, non-sensitive files pass through
- Blocks writes to sensitive files
