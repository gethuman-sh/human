# Background Daemon

A long-running background service that holds your tracker credentials once and answers `human` commands for you, so individual commands run instantly without re-authenticating each time.

- Runs `human` commands without per-call setup
- Holds tracker tokens once on the host
- Routes commands to the right project automatically
- Fetches issues from all configured trackers
- Completes browser OAuth sign-in flows automatically
- Surfaces ready-for-review handoffs to the TUI
- Queues destructive operations as permission requests; an approval is a one-time grant the client redeems by re-submitting the command, and decisions stay queryable by ID for 24 hours
- Rejects clients older than the wire protocol it speaks with a clear "upgrade the human CLI" error, before any side effects
- Records tool activity for later statistics
- Derives each PM ticket's workflow-board placement and applies drag-to-trigger pipeline transitions for the desktop board
- Places idea-labeled tickets (`human/idea`, bare `idea`) in the board's Ideas column by label alone, without scanning their comments
- Quick-captures a title-only idea ticket via a dedicated route, labeled so it lands in the Ideas column
- Reads a ticket's engineering plan from its `[human:plan]` comment: the board derives the planned state from it and, without a separate engineering ticket, dispatches implementation and verification on the PM key itself
- Runs the board's agent-driven ideation chat (start/reply/status) and creates the resulting PM ticket on the PM-role tracker
- Runs a guided, one-question-at-a-time ideation mode with fixed/agent-generated option sets and an editable draft-approval step, reusing the same PM-role ticket creation as chat mode
- Runs ideation in evolve mode against an existing idea ticket: the finished draft rewrites that ticket in place — title and description replaced, idea label removed, key preserved
- Serves the desktop settings UI: returns the project's `.humanconfig` as a masked snapshot (vault references verbatim, literal secrets never leave the daemon) and writes single-key edits back with a comment-preserving YAML round-trip
