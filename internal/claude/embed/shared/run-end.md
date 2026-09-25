## The end of the run

Your run ends at its own summary marker. That is a state, not a suggestion: the
last things this run does are post its `[human:fix-summary]`, record its stage
outcome, and terminate.

Three rules follow, and each of them was paid for:

- **Everything you still have to say goes in the summary marker.** Never post a plain, un-markered ticket comment:
  the board cannot render one, the next stage cannot read one, and a person opening the ticket then has two
  accounts of the same run to reconcile. If something is worth saying after the summary is posted, post another
  summary marker — the newest wins.
- **Markers posted by later stages are not your business.** Once your run has no
  instructed step left that owns them — in board context, the moment your verdict
  is on the ticket; in a shipping run, the moment your own deploy step has handed
  the merge to the loop — another actor owns what comes next: the deploy, its
  fixer, the pull-request review loop. Their markers (`deploy-*`, `pr-*`) are
  their record, not a task list for you. Reading one your own step is told to read
  (a pull-request URL for your report, a `[human:deploy-failed]` your step must
  report as an honest end) is that step doing its job; anything past it is not. Do
  not investigate what a later stage reports, do not annotate it, and never file
  your own diagnosis of another stage's failure. A deploy that failed while you
  were writing your summary is the deploy stage's to answer.
- **Your container is still holding the checkout.** The branch you built lives
  in a bind-mounted tree the next stage works in, and that stage now waits for
  your container to end before it touches it. Every minute past your instructed
  end is a minute the machine spends waiting for you, so ending promptly is part
  of the work.

`human fsm where <TICKET_KEY>` is for deciding your OWN way out, before you end.
After your summary marker there is no way out left to take — the run is over.
