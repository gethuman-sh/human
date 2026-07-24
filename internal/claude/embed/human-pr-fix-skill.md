---
name: human-pr-fix
description: Address a PR's review comments — dispatch the fixer to change the code and push to the branch
argument-hint: <key> --pr=<number> --branch=<branch>
---

`$ARGUMENTS` is `<KEY> --pr=<number> --branch=<branch>` — the PM ticket key, the open pull request whose review comments need addressing, and its branch. Parse them, then delegate to the **human-pr-fixer** agent.

Run the fixer at the `sonnet` tier: implementing a fix is visible-failure work — a red re-review or a failed check catches what it misses — so the expensive tier is not warranted here.

```
Task(subagent_type="human-pr-fixer", model="sonnet", prompt="Address review comments on PR <number> for ticket <KEY> --branch=<branch>")
```

The agent reads the PR's open review comments (the machine reviewer's **and any a human left out of band**), addresses them, pushes to the branch, and records its exit (`done | needs-input`) in `stage.pr-fix`. The daemon re-reviews after a `done` and escalates on `needs-input`, so you do **not** post board markers or decide the next step yourself: run the agent and report what it changed.
