# SC-3613 dist-drift fixtures

Frozen bytes of `desktop/frontend/dist/board.js`, used by
`scripts/dist-guard.test.mjs`. Do not regenerate, reformat, or lint them — the
whole point is that they are the exact bytes CI compared.

| File | Blob | Commit | Bytes |
|---|---|---|---|
| `board.committed.js` | `df0b06e37a62b469dc365c841229bac58c992d2b` | `99aea5e6^` (what main had committed) | 159863 |
| `board.rebuilt.js` | `1ff98ceded43827d6720e7c4baf8bf40bd7d00ef` | `99aea5e6` (what the build emits) | 159865 |

The pair differs on two lines by one trailing space each. `git diff --exit-code`
called that drift and blocked the deploy (SC-3613); the guard must not.
The test derives its semantic-drift fixture from `board.rebuilt.js` in memory by
reverting one `runGuardedAction(...)` call site to a bare `.catch(...)`.
