# Project Configuration

A single `.humanconfig.yaml` file tells `human` which issue trackers and code forges you work with and how to reach them. It pulls credentials from the file, your environment, or a vault, so the right connection is ready whenever you run a command.

- Reads your `.humanconfig.yaml` from the working directory
- Keeps machine-local overrides in a separate `local/` folder
- Runs fine even with no config file present
- Configures multiple named trackers and forges at once, grouped by what a backend is rather than who makes it: one `trackers:` list whose entries carry `kind: github`, `kind: jira`, … alongside the `forges:` list, which always had that shape
- Reads the older per-vendor sections (`githubs:`, `jiras:`, …) exactly as before, and will keep doing so — a working config never has to be rewritten. `human config migrate --group` folds them into the one list when you want it, moving the entries so comments and field order come along
- Overrides any token from an environment variable
- Targets a specific named instance with per-instance variables
- Resolves `1pw://` vault references so tokens stay secret
- Skips entries missing credentials instead of failing outright
- Reads through one object. Every question about the file — a provider's section, the project name, whether this machine drives the board — is asked of the same parsed document, and the parse is cached on size and modtime because the file is read constantly and edited while the daemon runs
- Refuses to write a change that breaks the configuration, and only what the change introduced: a fault the file already had is not this edit's doing, and blocking on it would stop someone fixing an unrelated setting
- Holds the whole file as one object (`config.Document`) that can be read as typed entries, changed through methods that say what they are for (`AddTracker`, `AddForge`, `RemoveTracker`, `MoveTrackerToForge`), checked against itself, and written back without losing a comment, an ordering, or a section this binary has never heard of
- Checks a configuration against itself (`human config check`), including what two sections say **together** — the kind of rule that previously had nowhere to live and ended up smuggled into a migration command or hand-hung on one provider's loader
- Separates "will this load" from "what will this do": a missing credential is an error, and a configuration that works while quietly costing an API quota is a warning — the class no loader can catch, because it is a prediction about behaviour rather than a shape
- Sets the model every agent container for this project runs, so a team can run cheap by default on a small machine and expensive where it matters

## Agent model

```yaml
agent:
  model: sonnet     # opus | sonnet | haiku | fable
```

`agent.model` is the **top-level** model an agent container starts with — the one a sub-agent dispatch inherits when it names none. Absent, containers run whatever the account defaults to, exactly as before.

Lowering it is safe because it cannot silently demote a judgment: every dispatch the tool ships names its tier explicitly (`internal/claude/embed/shared/model-tiers.md`), enforced by `TestPrompts_EveryDispatchNamesATier`, so each one keeps running where the policy puts it whatever the container default is — and raising it cannot silently promote one either. A value this binary does not recognise is ignored — containers fall back to the account default — and `human config check` reports it.
