# Project Configuration

A single `.humanconfig.yaml` file tells `human` which issue trackers and code forges you work with and how to reach them. It pulls credentials from the file, your environment, or a vault, so the right connection is ready whenever you run a command.

- Reads your `.humanconfig.yaml` from the working directory
- Keeps machine-local overrides in a separate `local/` folder
- Runs fine even with no config file present
- Configures multiple named trackers and forges at once
- Overrides any token from an environment variable
- Targets a specific named instance with per-instance variables
- Resolves `1pw://` vault references so tokens stay secret
- Skips entries missing credentials instead of failing outright
- Holds the whole file as one object (`config.Document`) that can be read as typed entries, changed through methods that say what they are for (`AddTracker`, `AddForge`, `RemoveTracker`, `MoveTrackerToForge`), checked against itself, and written back without losing a comment, an ordering, or a section this binary has never heard of
- Checks a configuration against itself (`human config check`), including what two sections say **together** — the kind of rule that previously had nowhere to live and ended up smuggled into a migration command or hand-hung on one provider's loader
- Separates "will this load" from "what will this do": a missing credential is an error, and a configuration that works while quietly costing an API quota is a warning — the class no loader can catch, because it is a prediction about behaviour rather than a shape
