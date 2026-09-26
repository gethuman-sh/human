---
name: human-deploy-fix
description: Recover a failed deploy — dispatch the deploy fixer to rebase onto the base, resolve conflicts, and fix failing CI on the branch
argument-hint: <key> --pr=<number> --branch=<branch>
---

`$ARGUMENTS` is `<KEY> --pr=<number> --branch=<branch>` — the PM ticket key, the open pull request whose deploy failed (a failing CI check a code change can turn green, or a rebase conflict against the base), and its branch. Parse them, then delegate to the **human-deploy-fixer** agent.

Run the fixer at the `sonnet` tier: recovering a deploy is visible-failure work — the re-run deploy's CI gate catches what it misses — so the expensive tier is not warranted.

```
Task(subagent_type="human-deploy-fixer", model="sonnet", prompt="Recover the failed deploy of PR <number> for ticket <KEY> --branch=<branch>", run_in_background=false)
```

The agent rebases the branch onto the base, resolves conflicts, fixes the failing lint/tests, leaves the result on the local branch ref, and records its exit (`done | outage | needs-input | needs-human-work`) in `stage.deploy-fix`. After a `done` the daemon publishes that branch with the host's credentials — the fixer's container has none — and re-runs Deploy. An `outage` is not a failure: the daemon posts `[human:deploy-outage]`, the card reads paused, no round is charged, and the deploy is re-driven when the substrate returns — unless this machine's own proxy policy refused the host, which it cannot tell from a dead network inside the container: the daemon recognises that from its own block record and reds the card once, naming the host and the `proxy.domains` line to add. Anything else reds the card. So you do **not** push, post board markers, or re-trigger Deploy yourself: run the agent and report what it changed.
