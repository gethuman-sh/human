export function reconcilePending(pending, fetchedKeys, fetchedTitles) {
    return pending.filter((p) => {
        if (p.key !== undefined) {
            return !fetchedKeys.has(p.key);
        }
        return !fetchedTitles.has(p.title);
    });
}
export function dropPending(pending, target) {
    return pending.filter((p) => p !== target);
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
export function applyPendingMoves(cards, moves, now) {
    if (moves.length === 0)
        return { cards, moves };
    const byKey = new Map(cards.map((c) => [c.key, c]));
    const held = [];
    for (const move of moves) {
        const card = byKey.get(move.key);
        if (!card || now >= move.expiresAt)
            continue;
        if (card.stage !== move.from)
            continue; // confirmed, or overtaken
        card.stage = move.to;
        card.state = move.state;
        held.push(move);
    }
    return { cards, moves: held };
}
