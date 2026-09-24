# Cost Ledger

The cost ledger is the durable record of what each ticket cost — in dollars and
in time — over its whole life, including runs that failed and work done twice.

The workspace already knew its total spend, but only workspace-wide, on a
separate page. This package puts the number where the work lives: each board
ticket carries its own cost and its own elapsed time, on the card's detail panel.

Every attributed request the proxy boundary records on the model host (a
request bound to a ticket and stage) is appended here as one row holding the
endpoint it hit, the status it got, the four token classes — fresh input,
output, cache-create, cache-read — the model that priced them, and the
request's duration. Only a completion (`POST /v1/messages`) is a call: a 2xx
one is priced, or counted as unmeasured when its usage could not be read; a
non-2xx one is counted as failed. The host's other endpoints — startup fetches,
token counting, telemetry — are kept for the record and counted as nothing. Nothing is capped per ticket and nothing is dropped on a
daemon restart, so a ticket built three times reads as three times the cost.

Dollars are computed at read time from the single rate card (`internal/claude`),
never stored, so historical tickets re-price correctly when a rate changes. A
read rolls the rows up into a whole-life total, the context-vs-answers split, and
a per-stage breakdown. A ticket with no recorded spend says so plainly rather
than showing a confident `$0.00`.

The store lives in `~/.human/costledger.db` (SQLite, WAL) alongside the other
daemon databases and is pruned past a generous retention window.

The same record is readable from the command line: `human stats tickets` ranks
the tickets that cost the most over a window, and `human stats tickets <KEY>`
shows one ticket's whole-life roll-up per stage — the numbers the card detail
shows, from the same ledger, scoped to the same project.
