# Cross-Tracker Operations

`human` talks to every supported issue tracker — Jira, Linear, GitHub, GitLab, Shortcut, Azure DevOps, ClickUp — through one consistent set of commands. You work with issues the same way no matter which backend a team uses.

- List a project's open or all issues anywhere
- Read full issue details by key or pasted URL
- Create new issues on any configured tracker
- Create bug tickets tracker-agnostically: a bug-typed create maps to each backend's native defect marker — issue type Bug on Jira and Azure DevOps, story type bug on Shortcut, the bug label/tag on Linear, GitHub, GitLab, and ClickUp
- Read and add comments on an issue
- Link two related issues ("relates to"; on GitHub, recorded as a cross-reference comment)
- Move an issue to a new status
- Finish or close an issue semantically (`human done KEY`, `human close KEY`) — the done/closed-type status is picked from the workflow, no status name needed
- Promote an idea ticket (`human idea promote KEY`) — strips the `human/idea`/`idea` labels, keeping key and history
- Assign an issue to a user
- Edit an issue's title and description
- Auto-detects the right tracker from a key
- Resolves tracker roles and topology (`human tracker topology`): which tracker is PM, which is engineering, single vs split
- Guards deletes and edits with safe-mode policies
