# Background Daemon

A long-running background service that holds your tracker credentials once and answers `human` commands for you, so individual commands run instantly without re-authenticating each time.

- Runs `human` commands without per-call setup
- Holds tracker tokens once on the host
- Routes commands to the right project automatically
- Fetches issues from all configured trackers
- Completes browser OAuth sign-in flows automatically
- Surfaces ready-for-review handoffs to the TUI
- Pauses for confirmation on destructive operations
- Records tool activity for later statistics
- Derives each PM ticket's workflow-board placement and applies drag-to-trigger pipeline transitions for the desktop board
- Runs the board's agent-driven ideation chat (start/reply/status) and creates the resulting PM ticket on the PM-role tracker
- Runs a guided, one-question-at-a-time ideation mode with fixed/agent-generated option sets and an editable draft-approval step, reusing the same PM-role ticket creation as chat mode
