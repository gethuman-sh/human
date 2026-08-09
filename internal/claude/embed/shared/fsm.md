## Ask the machine before you stop

The pipeline you are running inside is a state machine, and `human` can answer
questions about it. Ask it rather than guessing what to post, and rather than
stopping to ask a person:

```sh
human fsm where <TICKET_KEY>   # where this item is, and every way out — with a command for the ones that are YOURS
human fsm marker <name>        # what a marker records, where it moves an item, what fields it requires
human fsm constants            # the real budgets: retries, graces, bounds
```

`where` is the one to reach for. You know your ticket key; you do not need to
know which state you are in, because that is what it tells you — along with what
must be true while an item sits there, who may move it, and what happens if
nobody does.

It also tells you where the item has **been**. `history` lists the states it
passed through with times, oldest first, and stops before now; `entered` says
when it reached where it is. Read them before repeating work: they are how you
tell a first attempt from a third, which the retry budget does not show because
it counts stage relaunches and not review rounds. History is the trail only —
where you are **now** is `state` (or `candidates`), never the last history entry.

**Only a way out marked `"yours": true` carries a `command`.** That is the point
of asking. The rest are listed so you can see what waiting buys you and who you
are waiting for — the daemon, or a person — but they are not yours to take.
Posting another actor's marker does not advance the item; it puts it in a state
nobody drove it to, and everything downstream then reasons about a run that never
happened.

Three rules follow, and none of them is style:

- **Never invent a marker or a field.** The `command` you are given is complete,
  including every field that marker requires. A marker missing a required field
  is rejected; a marker you made up is worse, because it is accepted and means
  nothing.
- **Read `if_nothing_happens` before concluding you are stuck.** Most states are
  recovered by the daemon on a timer, and the field says which one and how long.
  Waiting is often the correct action and is not the same as being blocked.
- **Before you raise `[human:options]`, check your own ways out.** A decision
  block stops the pipeline and waits for a person, so it is the escape hatch, not
  a way of being careful. Raise it only for a genuine fork — something the
  evidence cannot settle and that you are not entitled to choose.

If `where` reports `candidates` rather than a single `state`, it could not tell
which phase you are in from the ticket alone. Pick the one whose `holds`
describes your run, and use that one's ways out — not the union of all of them.
