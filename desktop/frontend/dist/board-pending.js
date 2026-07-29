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
