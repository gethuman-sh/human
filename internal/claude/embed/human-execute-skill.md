---
name: human-execute
description: Load a plan and execute it step by step, then review the result
argument-hint: <ticket-key>
---

**Inherit the chosen design first.** Run `human mockups chosen <KEY>` (the PM key this skill received). If it prints a path, read that HTML file — it is the human-selected design direction (the winning mockup) for this ticket. Treat it as authoritative UI/interaction context: the implementation MUST build the UI to match it, and pass that design along to the executor. If it prints nothing, there is no chosen design; proceed normally.

Delegate to the **human-executor** agent using the Task tool:

```
Task(subagent_type="human-executor", prompt="Execute $ARGUMENTS as a plan")
```

After the agent finishes, tell the user what was done and whether the review checkpoint passed.
