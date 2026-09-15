// Pure predicates/builders for the Product-Backlog "Recreate description"
// action (SC-4923), kept free of DOM and Wails bindings so they can be unit-
// tested directly (mirrors board-descedit.ts).
// recreateAllowedFor is the Recreate-description gate. It deliberately mirrors
// descEditAllowedFor's lane and kind rules minus the justPromoted widening: a
// card the board still renders in Ideas has a drafter that is already free to
// write it, so offering a manual recreate there duplicates the background
// behaviour. Lanes after Product Backlog are excluded because a description
// there is the input to a [human:plan] marker, and rewriting it would silently
// invalidate the plan.
export function recreateAllowedFor(queue, bug = false, security = false) {
    if (bug || security)
        return false;
    return queue === "product";
}
// recreateConfirmBody is the dialog's text. It names replacement explicitly —
// a concurrent edit in another pane is caught by the user reading what is about
// to be discarded, which is the whole concurrency story here. Recovery of the
// replaced text is the tracker's own description history.
export function recreateConfirmBody(key) {
    return `The current description of ${key} will be replaced by a freshly written one. The text that is there now is discarded — the tracker's own description history is the only way back.`;
}
