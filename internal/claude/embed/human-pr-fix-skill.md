---
name: human-pr-fix
description: Address a PR's review findings — dispatch the fixer to change the code and commit on the branch
argument-hint: <key> --pr=<number> --branch=<branch>
---

`$ARGUMENTS` is `<KEY> --pr=<number> --branch=<branch>` — the PM ticket key, the open pull request whose review findings need addressing, and its branch. Parse them, then delegate to the **human-pr-fixer** agent.

Run the fixer at the `sonnet` tier: implementing a fix is visible-failure work — a red re-review or a failed check catches what it misses — so the expensive tier is not warranted here.

```
Task(subagent_type="human-pr-fixer", model="sonnet", prompt="Address review comments on PR <number> for ticket <KEY> --branch=<branch>", run_in_background=false)
```

The agent reads the machine reviewer's findings from `stage.pr-review` (**and any comment a human left on the PR out of band**), addresses them, commits on the local branch — it does not push; the daemon ships the branch at merge — and records its exit (`done | needs-input`) plus the post-fix head in `stage.pr-fix`. The daemon re-reviews the new commit after a `done` and escalates on `needs-input`, so you do **not** post board markers or decide the next step yourself: run the agent and report what it changed.
