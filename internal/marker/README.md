# Marker Protocol

The `[human:*]` marker comments are how pipeline stages hand work to each other on a ticket — plan attached, ready for review, review verdict, deploy result. This package is the single shared grammar for building and reading them, surfaced as `human marker post|show|list`.

- Posts any marker with validated fields (`human marker post KEY TYPE --field k=v --head TOKEN --body/--body-file`)
- Reads the newest marker of a type as parsed JSON or verbatim (`human marker show KEY TYPE [--raw]`)
- Lists every marker on a ticket, newest first (`human marker list KEY`)
- Enforces required fields and head-token enums for known types at post time
- Accepts unknown marker types so new pipeline stages need no CLI release
- Latest-wins semantics: a re-posted marker supersedes older ones without history edits
- Review handoffs get dedicated commands (`human handoff post|show KEY`): branch, commits, and daemon id are derived from git and the environment, and posting verifies every commit is reachable on the branch
