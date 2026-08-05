## Dependents: what else depends on the thing you are changing

Enumerating the **callers** of a function is not enumerating its dependents. The
dependencies that actually break have no symbol and no call edge: a stored format
read by line position, a value in a closed vocabulary handled by a switch
somewhere else, a convention duplicated in a sibling prompt. A caller search
returns clean for all three while the change ships broken — that is not a
hypothetical, it is how three regressions in one week reached a person weeks
later instead of the run that caused them.

So classify the shared thing you are changing by **kind**, and run that kind's
query. A change is often more than one kind. The kind chooses the query; a query
borrowed from another kind is not evidence.

| Kind | You are changing… | Query that finds its dependents |
|---|---|---|
| **function/type** | a declared function, method, type or interface | `human codenav impact <qname> --depth 2` |
| **closed set of values** | one member of a vocabulary: an exit class, a verdict, a status, a marker type, a stage name, a serialized field name | a literal search for the value across the code, **and** across your project's prompt/instruction files (e.g. `internal/claude/embed/*.md` in this codebase): `rg -n '<the literal>'` then `rg -n '<the literal>' <your prompt/instruction directory>`. A vocabulary lives in prose as often as in a switch |
| **stored format** | what gets written into an artifact someone else reads: a comment/marker body, a state record, a file format, output another component parses | every reader of the format, **tests included** — `rg -n '<a distinctive literal from the format>'`, then read each hit and ask whether it reads by **position** (line index, field order, offset) rather than by name |
| **instruction/convention** | a rule stated in one prompt or doc that a sibling states too | `rg -n '<a distinctive phrase from the rule>' internal/claude/embed/` — the siblings that say the same thing in different words |

**Bound the blast radius.** `human codenav impact <qname>` with no bound returns
hundreds of transitive callers for a hub symbol (276 for one symbol in this
repo), and a list that long gets pasted and ignored. Always pass `--depth 2`.

**Never use `human codenav impact --diff`.** It is forced to run locally against
a local index a pipeline container does not have, and fails there with
`apply schema: unable to open database file`. Only the named-symbol form —
`human codenav impact <qname> --depth 2` — is forwarded and works.

**`no changed/seed symbols found` is not "no dependents".** It exits 0 and means
codenav did not resolve the seed. Find the qualified name
(`human codenav search <name> --symbols`) and retry, or fall back to
`rg -n '\b<Name>\b'`. If neither answers, record it as unchecked — never as none.

**Every entry names its query and its result.** A dependent asserted without the
command that found it is an opinion:

```
- thing: the exit value `outage` — kind: closed set of values
  query: rg -n '"outage"' . && rg -n 'outage' internal/claude/embed/
  result: internal/daemon/board_retry.go:34 (the ExitOutage constant),
          internal/daemon/board_retry.go:157 (the exit-class switch),
          human-autofix-skill.md:55 (the vocabulary restated in prose)
```

**Carry each dependent to a disposition.** A list nobody acts on is decoration.
Whoever changes the code states, for every dependent on the list, exactly one of:

- `examined-and-unchanged: <dependent> — <why it is correct as it stands>`
- `examined-and-changed: <dependent> — <file:line of the change that keeps it correct>`

A dependent that is neither is an **unfinished change**, not a finished one, and
a reviewer reads it as incomplete.

**Say `unchecked` out loud.** A kind whose query you could not run or could not
resolve is recorded explicitly — `unchecked: <kind> — <why>` in the Dependents
list, and in the `unchecked` field of your stage record when you write one.
Silence reads as "no dependents", and that reading is how every one of these
regressions shipped.
