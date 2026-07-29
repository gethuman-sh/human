// Pure, DOM/Wails-free matching logic for reconciling and rolling back
// optimistic "pending" placeholder cards shown while a create (bug/security/
// idea) is in flight. Kept free of DOM and Wails bindings, like
// board-actions.ts/board-queue.ts, so it can be unit-tested directly.
//
// board.ts used to reconcile and roll back placeholders by *title* string
// equality alone. That misfires three ways: duplicate titles let one real
// card clear two placeholders, a failed create rolls back every placeholder
// sharing the title, and any tracker-side title normalization strands the
// placeholder as a ghost until app restart (SC-1691). The daemon already
// returns the created ticket's key, so a placeholder that captured a key
// hands over strictly on key; only a placeholder that never captured a key
// (a lost/racy response) falls back to title matching, so it isn't stranded.
export interface Pending {
  title: string;
  key?: string;
}

export function reconcilePending<T extends Pending>(
  pending: T[],
  fetchedKeys: Set<string>,
  fetchedTitles: Set<string>,
): T[] {
  return pending.filter((p) => {
    if (p.key !== undefined) {
      return !fetchedKeys.has(p.key);
    }
    return !fetchedTitles.has(p.title);
  });
}

export function dropPending<T>(pending: T[], target: T): T[] {
  return pending.filter((p) => p !== target);
}
