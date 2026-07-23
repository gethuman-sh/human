## Choosing a model per subagent

You choose the model when you dispatch. Pass it on the `Task` call — it overrides the agent's own frontmatter for that one call:

```
Task(subagent_type="human-bug-fixer", model="sonnet", prompt="…")
```

Valid values are exactly `opus`, `sonnet`, `haiku`, `fable` — aliases, not model ids. Omit the parameter to inherit.

**The rule: cheap models gather evidence, expensive models rule on it.**

Ask what a wrong answer costs *and whether anyone would notice*:

| Tier | Use for | Why |
|---|---|---|
| `opus` | Root-cause analysis, planning, review verdicts, adversarial challenges, deploy decisions | A wrong answer here is **silent** — a plausible-but-shallow cause, a review that says "pass" without looking. Nothing downstream catches it. |
| `sonnet` | Implementing a fix, verification runs, scanner fleets, mechanical edits | Failure is **visible**: a red test, a failed lint, a finding triage rejects. The check catches what the model misses. |
| `haiku` | Extraction, reformatting, classifying a failure as flake vs. real | Pure transformation, no judgment. |

Two rules that override the table:

1. **Never tier down an adversary.** A challenge or second opinion runs at `opus`. A weaker model gets talked out of its objection, which converts the check into a rubber stamp — worse than not running it, because it manufactures false confidence.
2. **Escalate on disagreement.** When a cheap-tier answer is contested — two runs differ, or a later stage contradicts an earlier one — re-ask that one question at `opus`. You then pay the expensive model only on the calls that turned out to be hard.

If you are unsure which tier fits, omit `model` and inherit. A deliberate inherit is better than a confident wrong tier.
