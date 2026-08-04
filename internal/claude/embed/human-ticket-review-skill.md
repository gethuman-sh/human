---
name: human-ticket-review
description: Review a ticket before work starts — does solving it solve the problem, completely and coherently — and remedy it in place
argument-hint: <ticket-key>
---

**Take ownership first.** Run `human assign <KEY>` (the ticket key this skill received) so the ticket records who is working it. It only sets ownership — no status change, so it never blocks on an approval gate. A failure here is not fatal: say so and carry on with the work.

Follow these steps in order:

1. **Review**: Delegate to the **human-ticket-reviewer** agent:

```
Task(subagent_type="human-ticket-reviewer", prompt="Review ticket $ARGUMENTS before it enters the pipeline. Judge whether solving it solves the problem — root or symptom, complete, coherent with existing siblings, right altitude. Act on what you find: reframe, supersede, escalate or reject. Record the verdict as a [human:ticket-review] marker.")
```

2. **Report** the verdict and what the agent did. The agent has already acted — it rewrote the framing,
   linked a duplicate, or created the design ticket. Do not ask the user to approve those; tell them what
   happened and name any ticket that was created or linked.

3. **When the verdict is `escalated`**, the created design ticket is the one that should be planned next.
   Say so, with its key.

4. **When the agent posted an options block instead of a verdict**, a genuine product fork needs the user.
   Present the choices as the agent wrote them and stop — the board and daemon carry it from there.

5. **Write** the review to `.human/ticket-review/<key>.md` where `<key>` is the ticket key lowercased
   (e.g. `KAN-1` → `kan-1.md`). Create the directory first with `mkdir -p .human/ticket-review`.

6. **Tell** the user: `Ticket review written to .human/ticket-review/<key>.md — verdict: <verdict>.`
