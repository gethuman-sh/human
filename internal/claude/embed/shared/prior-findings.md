## What past reviews found here

The machine reviewer's blocking findings are kept per file, ticket, PR and
round, with what the fixer did about each. Nothing consulted them, so a file
that drew the same finding on the last three tickets drew it again on the
fourth — the machine paying for one lesson once per ticket.

Before you change a file, ask:

```bash
human review findings <path> [<path> …] --key <TICKET_KEY>
```

`--key` is how the answer finds the right project; pass the ticket you are
working. The command advises — no gate reads it, and nothing is blocked by what
it returns. "No prior findings recorded for these files." is a real answer, not
a failure.

Each finding comes back with the reviewer's class for it, the ticket and round
it came from, and the fixer's disposition. A finding whose class you are about
to re-create is the one to read twice: the reviewer will find it again, and the
second time costs a review round.

**Say what you consulted.** For every file you touched, one line in the
artifact your stage produces — the plan, the handoff notes, the commit message:

```
prior-finding: <file> [<class>] <KEY> r<round> <disposition> — <what you did about it>
```

and when there were none:

```
prior-findings: none recorded for the files touched
```

Never quote the recorded finding text into a commit message: the log is public
and a finding can describe a defect that is still reachable. Name the file, the
class, the ticket, the round and your own one line.

The record covers the pull-request review loop. A file can have been reviewed
by the pre-merge reviewer and still have no rows here; that is a gap in the
record, not evidence the file was never criticised.
