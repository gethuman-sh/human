# Background Daemon

A long-running background service that holds your tracker credentials once and answers `human` commands for you, so individual commands run instantly without re-authenticating each time.

- Runs `human` commands without per-call setup
- Holds tracker tokens once on the host
- Routes commands to the right project automatically
- Fetches issues from all configured trackers
- Completes browser OAuth sign-in flows automatically
- Surfaces ready-for-review handoffs to the TUI
- Queues destructive operations as permission requests; an approval is a one-time grant the client redeems by re-submitting the command, and decisions stay queryable by ID for 24 hours — persisted in `~/.human/confirms.db`, so prompts and unredeemed grants survive daemon restarts
- Rejects clients older than the wire protocol it speaks with a clear "upgrade the human CLI" error, before any side effects
- Records tool activity for later statistics
- Derives each PM ticket's workflow-board placement and applies drag-to-trigger pipeline transitions for the desktop board
- Places idea-labeled tickets (`human/idea`, bare `idea`) in the board's Ideas column by label alone, without scanning their comments
- Launches the autonomous bug-fix pipeline (`/human-autofix`) on a bug ticket via a dedicated route for the desktop Bugs pane — no planning gate, and the fix chains into its review like any build
- Quick-captures a title-only idea ticket via a dedicated route, labeled so it lands in the Ideas column
- Files a defect ticket (title + description) via a dedicated route, bug-typed so every tracker marks it natively and the Bugs pane recognises the card
- Reads a ticket's engineering plan from its `[human:plan]` comment: the board derives the planned state from it and, without a separate engineering ticket, dispatches implementation and verification on the PM key itself
- Runs the board's agent-driven ideation chat (start/reply/status) and creates the resulting PM ticket on the PM-role tracker
- Runs a guided, one-question-at-a-time ideation mode with fixed/agent-generated option sets and an editable draft-approval step, reusing the same PM-role ticket creation as chat mode
- Runs ideation in evolve mode against an existing idea ticket: the finished draft rewrites that ticket in place — title and description replaced, idea label removed, key preserved
- Serves the desktop settings UI: returns the project's `.humanconfig` as a masked snapshot (vault references verbatim, literal secrets never leave the daemon) and writes single-key edits back with a comment-preserving YAML round-trip
- Surfaces the board detail panel's review findings, failure reason and fix summary by extracting them from the ticket's comments (`[human:review-complete]`, the latest `*-failed` marker, `[human:fix-summary]`) — comments only, never local files, so it works across every tracker backend
