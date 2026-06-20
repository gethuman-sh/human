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
