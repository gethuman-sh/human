---
name: human-ideate
description: Challenge a rough idea and create a ready PM ticket
argument-hint: <idea or topic>
---

Follow these steps in order:

1. **Parse** `$ARGUMENTS`:
   - Set `<topic>` to a slugified version (lowercase, spaces to hyphens, strip special chars).
   - If `$ARGUMENTS` is an existing ticket key whose ticket carries the `human/idea` label (bare `idea` also classifies), this run **evolves** that idea ticket in place instead of creating a new one: fetch it, note the key as `<IDEA_KEY>`, and use its title/description as the rough idea. Quick-captured ideas (e.g. from the board's Ideas column) are exactly such tickets. Otherwise the run creates a ticket from scratch as below.

2. **Create** the output directory: `mkdir -p .human/ideations`

3. **Phase 1 -- Context & challenge**: Delegate to the **human-ideator** agent:

   ```
   Task(subagent_type="human-ideator", prompt="Phase 1: Gather context and generate challenge questions for: $ARGUMENTS. Read the codebase, recent git history, and existing tickets for context. Return a context summary and 5 forcing questions.")
   ```

4. **Present** the agent's context summary to the user.

5. **Ask forcing questions** one at a time using `AskUserQuestion`. Ask each of the agent's 5 forcing questions individually. Collect all answers.

6. **Phase 2 -- Scope decision**: Delegate to the **human-ideator** agent with the collected answers:

   ```
   Task(subagent_type="human-ideator", prompt="Phase 2: Based on the challenge answers, propose scope. Answers: <paste all Q&A pairs>. Original idea: $ARGUMENTS. Return a problem statement, user story, acceptance criteria, and a scope recommendation (Expand / Hold / Reduce) with rationale.")
   ```

7. **Present** the scope recommendation to the user.

8. **Ask** the user to confirm or adjust the scope using `AskUserQuestion`: "The recommendation is to [Expand/Hold/Reduce] scope. Do you agree, or would you prefer a different scope direction?"

9. **Resolve PM tracker**: Run `human tracker topology` and use its `pm` entry: the entry's `type` as the tracker and its first configured project.

10. **Phase 3 -- Create ticket**: Delegate to the **human-ideator** agent with the scope decision, tracker, and project:

    ```
    Task(subagent_type="human-ideator", prompt="Phase 3: Create the PM ticket. Tracker: <tracker>. Project: <project>. Scope decision: <user's scope choice>. Problem statement, user story, and acceptance criteria from Phase 2: <paste Phase 2 output>. Create the ticket and add the challenge record as a comment.")
    ```

    When evolving an idea ticket, instruct the agent instead: "Phase 3: Evolve idea ticket <IDEA_KEY> in place — rewrite its title and description, remove the idea label, and add the challenge record as a comment." The key stays the same; no new ticket is created.

11. **Present** the created (or evolved) ticket key to the user.

12. **Write** the complete ideation record to `.human/ideations/<topic>.md`.

13. **Tell** the user: `Ideation written to .human/ideations/<topic>.md -- ticket created (or evolved) as <KEY>.`
