## Ask the machine before you stop

The pipeline you are running inside is a state machine, written down and compiled
into `human`. Ask it. It needs no daemon and no credentials, so it answers the
same in your container as on the host:

```sh
human fsm next <STATE>       # every way out: what records it, who causes it, which are YOURS
human fsm show <STATE>       # what must hold here, who may act, what happens if nobody does
human fsm marker <name>      # what a marker means, where it moves an item, what it requires
human fsm constants          # the real budgets — retries, graces, bounds
```

You are normally in **`<STATE>`**. A run that has moved on is in a different one;
`human fsm states` lists them all, and each carries a `holds` saying what is true
of an item sitting there.

**Only a way out marked `"yours": true` carries a `command`.** That is the whole
point of asking. The others are listed so you can see what waiting buys you and
who you are waiting for — the daemon, or a person — but you may not take them.
Posting another actor's marker does not advance the item; it puts it in a state
nobody drove it to, and the machine will then be reasoning about a run that never
happened.

Two rules follow, and they are not style:

- **Never invent a marker or a field.** The `command` a way out gives you is
  complete, including every field that marker requires. A marker missing a
  required field is rejected, and a marker you made up is worse — it is accepted
  and means nothing.
- **Before you raise `[human:options]`, check `next` for an edge you already
  own.** A decision block stops the pipeline and waits for a person, so it is the
  escape hatch, not a way of being careful. Raise it for a genuine fork only —
  something the evidence cannot settle and you are not entitled to choose.

If nothing looks like your way out, read `if_nothing_happens` before concluding
you are stuck. Most states are recovered by the daemon on a timer, and the field
says which one and how long — waiting is often the correct action, and it is not
the same as being blocked.
