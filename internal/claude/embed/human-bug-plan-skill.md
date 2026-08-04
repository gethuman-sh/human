---
name: human-bug-plan
description: Fetch a bug ticket and create a root-cause analysis with fix plan
argument-hint: <ticket-key>
---

**Take ownership first.** Run `human assign <KEY>` (the ticket key this skill received) so the ticket records who is working it. It only sets ownership — no status change, so it never blocks on an approval gate. A failure here is not fatal: say so and carry on with the work.

Delegate to the **human-bug-analyzer** agent using the Task tool:

```
Task(subagent_type="human-bug-analyzer", prompt="Analyze bug ticket $ARGUMENTS")
```

After the agent finishes, tell the user: `Bug analysis written to .human/bugs/<key>.md` (with the actual lowercased key).
