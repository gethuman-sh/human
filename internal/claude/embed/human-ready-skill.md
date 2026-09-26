---
name: human-ready
description: Fetch an issue tracker ticket, evaluate it against a Definition of Ready checklist, and update the ticket to make it ready
argument-hint: <ticket-key>
---

Follow these steps in order:

1. **Phase 1 — Evaluate**: Delegate to the **human-ready** agent:

```
Task(subagent_type="human-ready", model="opus", prompt="Phase 1: Evaluate readiness of ticket $ARGUMENTS")
```

2. **Present** the agent's assessment to the user.

3. **Phase 2 — Make Ready**: Delegate to the **human-ready** agent with the Phase 1 assessment and original ticket content:

```
Task(subagent_type="human-ready", model="opus", prompt="Phase 2: Make the ticket ready. Ticket key: $ARGUMENTS. Here is the Phase 1 assessment:\n\n<ASSESSMENT>\n<paste the full Phase 1 output here>\n</ASSESSMENT>\n\nFetch the ticket, generate an improved description that fills all gaps identified in the assessment, and update the ticket in the tracker using `human <tracker> issue edit`.")
```

4. **Write** the readiness assessment and improved description to `.human/ready/<key>.md` where `<key>` is the ticket key lowercased (e.g. `KAN-1` → `kan-1.md`). Create the directory first with `mkdir -p .human/ready`.

5. **Tell** the user: `Readiness written to .human/ready/<key>.md — ticket updated.`
