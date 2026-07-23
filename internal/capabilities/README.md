# Run Capabilities

What is this run allowed to do? `human capabilities` answers it in one place, so a pipeline agent stops carrying a branch per execution context in its prompt.

Before this, every skill had to describe two worlds at once — *"when BOARD_CONTEXT is true, do not push; the daemon's Deploy stage ships it"* — and each new deployment context added another conditional to twenty prompts. Now an agent reads its capability set and follows a single rule: **attempt nothing the set forbids, and treat a missing capability as a boundary, never as a failure.**

- Reports whether the run may push, open a pull request, and deploy (`human capabilities`)
- Emits the set as JSON for agents to store and branch on (`human capabilities --json`)
- Detects a board stage agent from the daemon's `board-<KEY>-<stage>` agent name — the same signal the daemon's failure watcher keys on
- Withholds every shipping capability in board context, because the container holds no push credentials and the board's Deploy stage owns shipping
- Withholds them too when the remote is unreachable — not merely unconfigured, but also configured with credentials that do not authenticate (`git ls-remote`, one round trip). It cannot prove *write* permission, so a push may still be refused; what it removes is the common case of having no credentials at all
- States **why** the set is restricted in one quotable line, so a stage that stops can say what stopped it
- Reports the workspace kind (`local` or `bind-mounted`)
- Fails safe: if the remote cannot be probed, the capability is withheld rather than assumed

## Why it runs locally

Unlike most commands, `capabilities` is **not** forwarded to the daemon. It describes the caller's own checkout, agent identity, and git remote — precisely what the daemon cannot see on the caller's behalf. A board container bind-mounts the repo, so its local git is the right thing to inspect.

## Conventional use

Preflight resolves the set once and stores it for the run:

```sh
human capabilities --json | human state set <KEY> capabilities --json --body-file -
```

Later stages read `capabilities` from [agent state](../agentstate/README.md) instead of re-detecting it, so every stage of a run agrees on what it may do.
