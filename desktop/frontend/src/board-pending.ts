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

// A move the person made that the machine has not confirmed yet.
//
// Dropping a card moves it at once, which is right — an action must be visible
// immediately. The refresh that follows can answer from before the drop,
// because the record of it is not readable yet, and replacing the card with
// that answer undoes what the person just did (SC-2521). Holding the move until
// a fetch confirms it is the same contract already used for a newly created
// ticket, whose placeholder survives a fetch until its key appears.
export interface PendingMove {
  key: string;
  // from is where the card sat before the drop. It is the ONLY fetched value
  // that counts as "not caught up yet" — see applyPendingMoves.
  from: string;
  to: string;
  state: string;
  expiresAt: number;
}

// MovableCard is the part of a card this reconciliation touches.
export interface MovableCard {
  key: string;
  stage: string;
  state: string;
}

// applyPendingMoves re-applies moves the fetch has not caught up with, and
// returns the moves still worth holding.
//
// A move is held only while the fetch still shows the card exactly where it was
// before the drop. Anything else ends it: the destination means the machine
// agreed, a third stage means the machine has moved on (a failure, a stage that
// advanced), and a card absent from the fetch has left the board. That rule is
// what keeps this from becoming a lie — it suppresses precisely one answer, the
// stale one, and yields to every other.
//
// Expiry is the backstop for a move that is never confirmed and never
// contradicted: a launch that silently did nothing must return to the truth
// rather than leave the board comfortable and wrong.
export function applyPendingMoves<T extends MovableCard>(
  cards: T[],
  moves: PendingMove[],
  now: number,
): { cards: T[]; moves: PendingMove[] } {
  if (moves.length === 0) return { cards, moves };
  const byKey = new Map(cards.map((c) => [c.key, c]));
  const held: PendingMove[] = [];
  for (const move of moves) {
    const card = byKey.get(move.key);
    if (!card || now >= move.expiresAt) continue;
    if (card.stage !== move.from) continue; // confirmed, or overtaken
    card.stage = move.to;
    card.state = move.state;
    held.push(move);
  }
  return { cards, moves: held };
}
