## Choosing a model per subagent

You choose the model when you dispatch. Pass it on the `Task` call — it overrides the agent's own frontmatter for that one call:

```
Task(subagent_type="human-bug-fixer", model="sonnet", prompt="…")
```

Valid values are exactly `opus`, `sonnet`, `haiku`, `fable` — aliases, not model ids. Omitting the parameter is not a fifth value: it runs the work on whatever the container happens to default to.

**The rule: cheap models gather evidence, expensive models rule on it.**

Ask what a wrong answer costs *and whether anyone would notice*:

| Tier | Use for | Why |
|---|---|---|
| `fable` | Adversarial challenges, root-cause verdicts — the judgments nothing downstream re-checks | A wrong answer is not merely silent, it is **argued**: an adversary that can be talked out of its objection, or a plausible-and-wrong root cause, manufactures confidence. The volume is small, so being wrong here is cheap to pay for. |
| `opus` | Planning, review verdicts, ticket review, finding triage, deploy decisions | A wrong answer here is **silent** — a plausible-but-shallow cause, a review that says "pass" without looking. Nothing downstream catches it. |
| `sonnet` | Implementing a fix, verification runs, scanner fleets, mechanical edits | Failure is **visible**: a red test, a failed lint, a finding triage rejects. The check catches what the model misses. |
| `haiku` | Extraction, reformatting, classifying a failure as flake vs. real | Pure transformation, no judgment. |

**`haiku` has no shipped dispatch today, and that is a measurement rather than an oversight.** Every step of this tool's pipelines that *looks* cheap — the recon, survey and attack-surface passes — is the sole input to six to ten dependent agents, so a wrong one is silent: that is the `opus` row, not this one. The row stays because `agent.model` accepts `haiku` as a project's container default and the next pipeline may have real extraction work. Moving a recon or survey step here to make the row non-empty buys a non-empty row with a silent wrong answer.

Three rules that override the table:

1. **Never tier an adversary DOWN.** A challenge or second opinion runs at `fable`, and never below `opus`. A weaker model gets talked out of its objection, which converts the check into a rubber stamp — worse than not running it, because it manufactures false confidence. The one adversary deliberately held at `opus` is `human-pr-reviewer`: at 108 spawns in 30 days it is the highest-volume dispatch in the pipeline, so moving it is a measurement someone has to take rather than a judgement to make from this table.
2. **Escalate on disagreement.** When a cheap-tier answer is contested — two runs differ, or a later stage contradicts an earlier one — re-ask that one question at `opus`. You then pay the expensive model only on the calls that turned out to be hard.
3. **A cheap fan-out is followed by a top-tier validator.** A pipeline that dispatches two or more agents below `opus` in one phase runs the phase that reads their output at `opus` or above. That validator is where rule 2's escalation actually happens — it is the thing that contests a cheap answer — so a fleet shipped without one has no escalation, only unreviewed output.

**Never omit `model` to avoid choosing.** Omitting it does not inherit a tier — it inherits whatever the account happens to default to, which is a decision nobody made and which `human stats subagents` cannot attribute. If the work produces a verdict, a plan, a root cause or a challenge, name `opus` or `fable`; if its failure is caught by a test, a lint or a triage, name `sonnet`. A tier you can defend beats a default you did not pick.
