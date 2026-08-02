---
name: human-deploy-fix
description: Recover a failed deploy — dispatch the deploy fixer to rebase onto the base, resolve conflicts, and fix failing CI on the branch
argument-hint: <key> --pr=<number> --branch=<branch>
---

`$ARGUMENTS` is `<KEY> --pr=<number> --branch=<branch>` — the PM ticket key, the open pull request whose deploy failed (a failing CI check or a rebase conflict against the base), and its branch. Parse them, then delegate to the **human-deploy-fixer** agent.

Run the fixer at the `sonnet` tier: recovering a deploy is visible-failure work — the re-run deploy's CI gate catches what it misses — so the expensive tier is not warranted.

```
Task(subagent_type="human-deploy-fixer", model="sonnet", prompt="Recover the failed deploy of PR <number> for ticket <KEY> --branch=<branch>")
```

The agent rebases the branch onto the base, resolves conflicts, fixes the failing lint/tests, leaves the result on the local branch ref, and records its exit (`done | needs-input | needs-human-work`) in `stage.deploy-fix`. After a `done` the daemon publishes that branch with the host's credentials — the fixer's container has none — and re-runs Deploy; anything else reds the card. So you do **not** push, post board markers, or re-trigger Deploy yourself: run the agent and report what it changed.
