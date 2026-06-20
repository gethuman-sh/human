# Secret Vault

Instead of pasting real API tokens into your config, you write a short reference like `1pw://Development/GitHub PAT/token`. `human` fetches the real secret from 1Password at startup, so credentials never sit in plaintext on disk.

- Use `1pw://vault/item/field` references in any token field
- Resolves references to real secrets at startup
- Supports 1Password as the secret provider today
- Unlocks via the 1Password desktop app prompt
- Falls back to the `op.exe` CLI on WSL2
- Passes plain non-secret values through untouched
- Never caches secrets, fetching them fresh each time
- Runs without vault resolution when none is configured
