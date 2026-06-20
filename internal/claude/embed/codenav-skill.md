---
name: codenav
description: Navigate code structure fast and token-frugally. Use whenever you need to find where a symbol is defined or used, who calls a function (or what it calls), trace a call path, gauge the blast radius of a change, or search code by symbol/text — i.e. go-to-definition, find-references, callers/callees, call graph, impact analysis. Prefer this over grep and reading whole files for structural questions.
allowed-tools: Bash(human codenav *)
---

# Code Navigation (codenav)

`human codenav` indexes a repository into a local SQLite database and answers structural questions about it precisely and cheaply. **Prefer it over `grep`/`rg` and reading whole files** when the question is "where is X defined / used / who calls it / what breaks if I change it".

## First: make sure the repo is indexed

Queries need an index. Run this once per repository (re-run after large changes):

```bash
human codenav index .
```

If any query reports the repo is not indexed, run the line above, then retry the query.

## Commands

```bash
human codenav def <name>            # go-to-definition (+ source). Add --outline for signature + location only (token-frugal)
human codenav refs <name>           # find references, each with its enclosing symbol and source line
human codenav callers <qname>       # who transitively calls this (--depth N)
human codenav callees <qname>       # what this transitively calls (--depth N)
human codenav callpath --from A --to B   # concrete call paths between two symbols
human codenav impact <qname>        # blast radius: transitive callers; or `--diff` for impact of uncommitted changes
human codenav search <query>        # full-text search over code (default) or symbol names (--symbols)
human codenav outline <file>        # symbols in a file with signatures, no bodies
human codenav overview              # architecture summary: kinds + most-called hub symbols (good cold-start)
```

Every command supports `--json` for structured output. Start an unfamiliar codebase with `overview`, then `outline`/`def`.

## If a command errors — try harder, don't fall back to grep

codenav is a normal CLI, so recover the way you would with `git` or `go`:

1. Read the error message — it usually says exactly what to do (e.g. "run: human codenav index .").
2. If you're unsure of flags or arguments, run `human codenav <subcommand> --help` and retry with corrected arguments.
3. If a symbol isn't found by `def`/`refs`, try `human codenav search <name> --symbols` to find the right qualified name, then retry.

Only fall back to `grep`/file reads if codenav genuinely cannot answer the question after these steps.
