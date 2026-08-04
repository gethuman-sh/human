# Secret Vault

Instead of pasting real API tokens into your config, you write a short reference like `1pw://Development/GitHub PAT/token`. `human` fetches the real secret from 1Password at startup, so credentials never sit in plaintext on disk.

- Use `1pw://vault/item/field` references in any token field
- Use `gh://token` (or `gh://<hostname>/token` for GitHub Enterprise) to resolve the GitHub CLI's keyring token — no PAT copying
- Resolves references to real secrets at startup
- Supports 1Password and the GitHub CLI as secret providers today; `gh://` works alongside any configured provider
- Resolves via the 1Password CLI (`op`) on every platform and every build (`op.exe` on WSL). The in-process SDK that used to precede it is gone: it reached the same desktop app by a second route, so it was a second implementation of the working path rather than a capability of its own
- Serves a resolved secret from memory for a bounded TTL, so an interactive store is asked once rather than once per command; concurrent readers of one reference share a single resolution, and the cached value is held in locked, dump-excluded memory
- In CGO-enabled developer builds, unlocks via the 1Password desktop app prompt first, falling back to the CLI automatically
- Passes plain non-secret values through untouched
- Fetches every secret fresh while the store is reachable (so rotations are picked up immediately); a successfully resolved value is kept in daemon memory only as a lapse fallback, served only when a later fetch fails because the credential session lapsed — so work whose secret already resolved this run is not failed by an unrelated stale read
- Bounds that fallback with a sliding idle window (`cache_ttl`, default 15m, in memory only, never written to disk): every read refreshes the window, so a secret in continuous use never forces a fresh fetch and re-approval, while a secret nobody asks for is retired once the window lapses; set `cache_ttl: 0` to disable the fallback entirely and fetch strictly fresh every time
- Caps that sliding window with an absolute maximum lifetime (`cache_max_ttl`, default 24h): however continuously a secret is used, its sealed plaintext is dropped once this ceiling passes, so "in continuous use" never means "held forever" — tighten it for a stricter memory posture or lengthen it for unattended runs longer than a day
- Runs without vault resolution when none is configured
