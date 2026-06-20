# Command Flags

The shared rules that tell `human` which command options carry a value, so your commands are parsed the same way everywhere. This keeps a value like `--tracker work` from being mistaken for an action.

- Recognizes global options that take a value
- Parses your commands consistently across the tool
- Keeps option values from being read as commands
- Protects confirmation prompts from being skipped
- Covers tracker, GitHub, GitLab, and more
