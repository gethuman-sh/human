# Agent State

Durable working memory for pipeline agents, kept by the daemon and surfaced as `human state`. A stage records what it learned under a ticket scope; the next stage — or a fresh agent taking over from one that died mid-run — reads it back instead of re-deriving it from nothing.

This is the private half of the pipeline's memory. The `[human:*]` [marker comments](../marker/README.md) stay the public, human-readable record on the ticket; state holds the verbose internal context that would otherwise pollute it, and never leaves the machine it is written on.

- Stores any text or JSON value under a `(ticket scope, name)` pair (`human state set KEY NAME --value V | --body-file - | --json`)
- Reads a value back for scripting, with `--default` instead of failing when absent (`human state get KEY NAME`)
- Lists a scope's entries, filtered by name prefix, as a table or JSON (`human state list KEY --prefix budget.`)
- Keeps atomic counters for retry budgets, so a flaky failure need not consume an attempt (`human state incr KEY budget.fix.attempts`)
- Claims a stage for one agent and refuses a claim another agent still heartbeats (`human state claim KEY --stage fix`)
- Hands an abandoned stage to a fresh agent once the claim's TTL lapses, naming the displaced agent **and the state keys it left behind** so its work is inherited rather than redone
- Judges that lapse by the TTL the **holder** declared, not the challenger's: a slow stage sets a long `--ttl` and is not stolen, a short one is reclaimable quickly, and a successor never has to guess its predecessor's heartbeat cadence
- Forces a handover of a live claim when an operator knows the holder is gone (`--takeover`)
- Shows who holds which stage of a ticket (`human state claims KEY`)
- Releases a stage on a clean handoff, and only for the agent that holds it (`human state release KEY --stage fix`)
- Prunes state past its retention window, hourly in the daemon and on demand (`human state prune --older-than 14d`)
- Records who wrote each value (agent name and run id), read from the forwarded request environment so a board container's identity survives

## Where the data lives

`human state` is deliberately **not** a local subcommand, so it is forwarded to the daemon and executes there against `~/.human/state.db` on the daemon host. That is what makes the store shared: every board container, every agent, and the host CLI see the same database. Making the command local would silently give each caller a private one.

Because the command runs inside the daemon's own command tree, a daemon binary predating `human state` cannot serve it — rebuild and restart the daemon after upgrading.

## Conventional names

The namespace is open (any name is allowed, like the marker protocol), but these carry agreed meaning:

| Name | Written by | Purpose |
|---|---|---|
| `stage.<name>` | every stage | JSON stage report: status, evidence, blockers, next |
| `decisions` | preflight | answers to the questions asked once, up front |
| `budget.<stage>.attempts` | fixer, verify | retry counter |
| `budget.<stage>.flakes` | fixer, verify | failures classified as flaky, not charged as attempts |
| `capabilities` | preflight | what this run may do (push, open a PR, deploy) |
| `<stage>.evidence` | any stage | the context that would otherwise die at a handoff |

## Limits

Values are capped at 256 KiB: state is memory another agent reads back in full, not a file store. An agent with a larger payload should leave a path behind instead. Entries are pruned after 14 days without an update.
