# Cross-Tracker Operations

`human` talks to every supported issue tracker — Jira, Linear, GitHub, GitLab, Shortcut, Azure DevOps, ClickUp — through one consistent set of commands. You work with issues the same way no matter which backend a team uses.

- List a project's open or all issues anywhere
- Read full issue details by key or pasted URL
- Create new issues on any configured tracker
- Create bug tickets tracker-agnostically: a bug-typed create maps to each backend's native defect marker — issue type Bug on Jira and Azure DevOps, story type bug on Shortcut, the bug label/tag on Linear, GitHub, GitLab, and ClickUp
- Classify security tickets tracker-agnostically (`IsSecurity`, mirroring `IsBug`): a `security` token in the type or any label marks a vulnerability, disjoint from bug. No backend has a universal native security type, so a security-typed create carries the `security` label, which every tracker recognises
- Read and add comments on an issue
- Link two related issues ("relates to"; on GitHub, recorded as a cross-reference comment), and remove a link again
- Record which of two tickets must finish first (`human link A B --blocks`) and read the ordering back, so work that waits is not started. Only backends that can express direction accept it — the rest refuse rather than storing a weaker link that would look the same and order nothing
- Move an issue to a new status
- Finish or close an issue semantically (`human done KEY`, `human close KEY`) — the done/closed-type status is picked from the workflow, no status name needed
- Promote an idea ticket (`human idea promote KEY`) — strips the `human/idea`/`idea` labels, keeping key and history
- Assign an issue to a user, or take ownership of one yourself (`human assign KEY`) — ownership only, no status change, so an unattended stage can record who is working a ticket without tripping a gate
- Edit an issue's title and description
- Auto-detects the right tracker from a key
- Resolves tracker roles and topology (`human tracker topology`): which tracker is PM, which is engineering, single vs split
- Configures issue trackers, and only issue trackers. A `githubs:` entry is a tracker; the code host that opens pull requests is a `forges:` entry, loaded by `internal/forge` into its own type. Nothing here carries a forge, so nothing here has to ask whether a backend is one — the predicate, the filter and the credential skip that used to keep the two apart are gone with the union that needed them ([SC-3876]). A config predating the split is migrated by `human config migrate`, and a `githubs:` entry still declaring `role: forge` fails the load with that instruction rather than becoming a tracker with a role that no longer exists
- Guards deletes and edits with safe-mode policies
