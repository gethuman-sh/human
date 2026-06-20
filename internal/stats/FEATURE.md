# Activity Statistics

Keeps a rolling record of what AI agents do through `human`, so you can look back at tool usage and trends over the past month.

- Records each tool call an agent makes
- Keeps a rolling 30-day history
- Answers usage and trend questions on demand
- Writes activity in the background without slowing work
- Drops events under load instead of stalling
- Stores everything locally in SQLite
