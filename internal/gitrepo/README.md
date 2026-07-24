# Git Repository

Lets `human` read facts about the git repository you are working in, so it can figure out where your code lives without you spelling it out. This is how `human` knows which forge and project to act on.

- Reads the origin remote URL of a repo
- Works on the current directory by default
- Targets any repository by path
- Feeds forge and project detection automatically
- Lists the commits referencing a ticket key in any accepted reference format (`human commits for KEY`), excluding merge-PR commits
- Prints the canonical bracket-style commit-message prefix for a ticket (`human commits prefix PM_KEY [ENG_KEY]`)
- Extracts the ticket keys referenced by commits touching given paths (`human commits keys [PATH...]`)
- Resolves the recency boundary — latest tag, else a 30-day window — and whether paths changed since it (`human commits recency`, `human commits touched`)
- Replays the deploy freshness rebase in an ephemeral worktree, attributing the replayed commits to the caller-supplied committer identity (the configured bot identity)
- Reports a clear error when no remote
