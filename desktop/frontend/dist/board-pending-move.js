export function reconcilePendingMoves(moves, fetchedStageByKey, now) {
    const kept = [];
    const overrides = [];
    for (const m of moves) {
        // A move that never confirms must not stick forever — bounded so the
        // board returns to the truth rather than showing a comfortable lie.
        if (now >= m.expiresAt)
            continue;
        const fetched = fetchedStageByKey.get(m.key);
        // An incomplete fetch (card not present) does not undo what the person
        // just did — hold it, but there is nothing to override.
        if (fetched === undefined) {
            kept.push(m);
            continue;
        }
        // Confirmed: the fetch caught up to the target. The real card now carries
        // the move, so the shield is no longer needed.
        if (fetched === m.toStage)
            continue;
        // Stale read still at the origin: hold the move and pin the fetched card
        // back to the target so the held card keeps its launched appearance.
        if (fetched === m.fromStage) {
            kept.push(m);
            overrides.push({ key: m.key, toStage: m.toStage });
            continue;
        }
        // Any third stage is the daemon changing its mind — it wins, no override.
    }
    return { moves: kept, overrides };
}
export function dropPendingMove(moves, key) {
    return moves.filter((m) => m.key !== key);
}
