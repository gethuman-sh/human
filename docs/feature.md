# Feature Outline

## CLI

- **Supported trackers**
  - Jira — issue keys `<PROJECT>-<number>` (e.g. `KAN-1`), project keys uppercase (e.g. `KAN`)
  - GitHub — issue keys `owner/repo#<number>` (e.g. `octocat/hello-world#42`), project keys `owner/repo`
  - GitLab — issue keys `namespace/project#<IID>` (e.g. `mygroup/myproject#42`), project keys `namespace/project`
  - Linear — issue keys `<TEAM>-<number>` (e.g. `ENG-123`), project keys uppercase (e.g. `ENG`)
  - Azure DevOps — issue keys `<Project>/<ID>` (e.g. `Human/42`), project keys are project names
  - Shortcut — issue keys are numeric story IDs (e.g. `123`), project keys are project names
- **Auto-detection** — tracker type is inferred from issue key format when possible (GitHub keys containing `/` and `#` are globally unique; other trackers require `--tracker` when multiple types are configured)
- **Quick commands** (auto-detect tracker from key format and configuration)
  - `get <key>` — fetch a single issue (auto-detects tracker)
  - `list --project=<project>` — list issues (auto-detects tracker)
- **Provider-specific commands**
  - `<tracker> issues list` — list issues for a project (requires `--project`)
  - `<tracker> issue get <key>` — fetch a single issue with metadata and description
  - `<tracker> issue create <title> --description <description>` — create a new issue (requires `--project`)
  - `<tracker> issue comment add <key> <body>` — add a comment to an issue
  - `<tracker> issue comment list <key>` — list comments on an issue
  - `<tracker> issue start <key>` — start working on an issue (transition to In Progress and assign to yourself)
  - `<tracker> issue delete <key>` — delete (or close) an issue by key (requires `--confirm`)
- **Utility commands**
  - `tracker list` — list configured tracker instances
  - `install --agent claude` — install Claude Code skills and agents
  - `browser <url>` — open a URL in the default browser (works in devcontainer via daemon)
-
## Skills / Agents

- **`/human-ready` skill** (`.claude/skills/human-ready/SKILL.md`)
  - Accepts `<ticket-key>` argument
  - Delegates to the `human-ready` agent to fetch and evaluate the ticket
  - Presents the agent's assessment, then asks the user to fill in each missing or partial item via `AskUserQuestion`
  - Writes the completed readiness assessment to `.human/ready/<key>.md` (lowercased key)
- **`/human-brainstorm` skill** (`.claude/skills/human-brainstorm/SKILL.md`)
  - Accepts `<ticket-key or topic>` argument
  - Two-phase process using the `human-brainstormer` agent:
    1. Phase 1: Gathers context (fetches ticket if key, explores codebase) and suggests clarifying questions
    2. Phase 2: Generates 2-3 implementation approaches with trade-offs, pros/cons, complexity estimates, and a recommendation
  - Asks clarifying questions one at a time via `AskUserQuestion`
  - User picks the approach, result is written to `.human/brainstorms/<identifier>.md`
- **`human-brainstormer` agent** (`.claude/agents/human-brainstormer.md`)
  - Tools: Bash, Read, Grep, Glob, Write
  - Phase 1 (Context gathering): Fetches ticket, explores codebase for relevant code and patterns, returns context summary with 3-5 clarifying questions
  - Phase 2 (Generate approaches): Incorporates clarification answers, generates 2-3 distinct approaches with affected files, pros/cons, complexity estimates, risks, and a recommendation
- **`/human-plan` skill** (`.claude/skills/human-plan/SKILL.md`)
  - Accepts `<ticket-key>` argument
  - Delegates to the `human-planner` agent with prompt `Create an implementation plan for ticket <key>`
  - Embeds the plan directly on the tracker (no plan files): as a separate engineering ticket's description in split topology, or as a `[human:plan]` comment on the ticket itself in single-tracker topology (`human plan show <key>` prints it back)
- **`/human-bug-plan` skill** (`.claude/skills/human-bug-plan/SKILL.md`)
  - Accepts `<ticket-key>` argument
  - Delegates to the `human-bug-analyzer` agent with prompt `Analyze bug ticket <key>`
  - Writes the analysis to `.human/bugs/<key>.md` (lowercased key)
- **`human-ready` agent** (`.claude/agents/human-ready.md`)
  - Tools: Bash, Read
  - Runs `human tracker list` to discover configured trackers, then fetches the ticket (`human get <key>` for single-tracker setups, or `human <tracker> issue get <key>` for multi-tracker setups)
  - Evaluates against the Definition of Ready checklist (6 criteria):
    1. Clear description — is the problem or feature clearly stated?
    2. Acceptance criteria — are there concrete, testable conditions for "done"?
    3. Scope — is the ticket small enough for a single implementation effort?
    4. Dependencies — are external dependencies or blockers identified?
    5. Context — is the "why" explained (user need, business reason)?
    6. Edge cases — are failure modes or boundary conditions mentioned?
  - Returns a structured report (summary, status table with present/partial/missing per criterion, missing-information questions) without prompting the user directly
- **`human-planner` agent** (`.claude/agents/human-planner.md`)
  - Tools: Bash, Read, Grep, Glob, Write
  - Planning process:
    1. Fetch the ticket via `human <tracker> issue get <key>` (or `human get <key>` for single-tracker setups)
    2. Explore the codebase with Glob, Grep, and Read to understand affected areas
    3. Identify existing patterns, conventions, and related code
    4. Produce a structured plan (context, ordered changes with exact signatures and step-by-step instructions, verification steps, test cases)
    5. Return the plan as output (attached by the orchestrator: engineering ticket description in split topology, `[human:plan]` comment otherwise)
- **`human-bug-analyzer` agent** (`.claude/agents/human-bug-analyzer.md`)
  - Tools: Bash, Read, Grep, Glob, Write
  - Analysis process:
    1. Fetch the ticket via `human <tracker> issue get <key>` (or `human get <key>` for single-tracker setups) and comments via `human <tracker> issue comment list <key>`
    2. Identify symptoms — error messages, stack traces, failing inputs, reproduction steps
    3. Locate code — use Grep and Glob to find functions in stack traces, error messages, related code paths, tests, and log statements
    4. Read and trace the code flow to identify root cause
    5. Write the analysis to `.human/bugs/<key>.md` (lowercased key) with summary, symptoms, root cause, affected code, fix plan, test plan, and related code
- **Integration with Claude Code**
  - `human install --agent claude` writes skills and agents to `.claude/skills/` and `.claude/agents/`
  - `--personal` flag installs to `~/.claude/` for user-wide availability
  - Installs six files: `human-plan` skill, `human-ready` skill, `human-bug-plan` skill, `human-planner` agent, `human-ready` agent, `human-bug-analyzer` agent
