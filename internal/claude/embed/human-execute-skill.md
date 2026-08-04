---
name: human-execute
description: Load a plan and execute it step by step, then review the result
argument-hint: <ticket-key>
---

**Take ownership first.** Run `human assign <KEY>` (the ticket key this skill received) so the ticket records who is working it. It only sets ownership — no status change, so it never blocks on an approval gate. A failure here is not fatal: say so and carry on with the work.

**Inherit the chosen design first.** Run `human mockups chosen <KEY>` (the PM key this skill received). If it prints a path, read that HTML file — it is the human-selected design direction (the winning mockup) for this ticket. Treat it as authoritative UI/interaction context: the implementation MUST build the UI to match it, and pass that design along to the executor. If it prints nothing, there is no chosen design; proceed normally.

Delegate to the **human-executor** agent using the Task tool:

```
Task(subagent_type="human-executor", prompt="Execute $ARGUMENTS as a plan", run_in_background=false)
```

After the agent finishes, tell the user what was done and whether the review checkpoint passed.
