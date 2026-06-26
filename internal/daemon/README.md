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
- Reports Docker-engine availability over the wire so the desktop board can gate agent launches without holding Docker access itself
- Serializes the wire types defined by the public [`human-daemon-client`](https://github.com/gethuman-sh/human-daemon-client) contract module, the single source of truth for the daemon protocol (the daemon aliases them and serves them unchanged)
