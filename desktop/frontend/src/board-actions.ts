// Shared "show error and stop" guard for board actions that call a fallible
// daemon RPC and then resync the board. Kept free of DOM and Wails bindings,
// like board-queue.ts/board-detail.ts, so it can be unit-tested directly.
//
// reconcile() (board.ts) always overwrites current.error with the *board
// fetch's* error, which is unrelated to (and almost always absent for) an
// action that itself just failed. Calling reconcile() right after showError()
// in the same failure branch therefore clobbers the banner before it's
// readable (SC-637). runGuardedAction makes the correct sequencing —
// onSuccess (typically reconcile) only runs when the action did not throw —
// the only way to run an action, generalizing the fix that was made once for
// closeTicket() (SC-195) but never carried to its sibling call sites.
export async function runGuardedAction(
  action: () => Promise<void>,
  onError: (err: unknown) => void,
  onSuccess: () => Promise<void>,
): Promise<void> {
  try {
    await action();
  } catch (err) {
    onError(err);
    return;
  }
  await onSuccess();
}
