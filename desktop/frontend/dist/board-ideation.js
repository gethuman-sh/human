// Pure ideation-panel state predicates, kept free of DOM and Wails bindings so
// they can be unit-tested directly. Mirrors the board-queue.ts / board-detail.ts seam.
// The terminal "done" state is intentionally excluded: once a ticket is created
// the panel auto-closes and must never present a live send button (SC-859).
export function ideationInputEnabled(state) {
    return state === "awaiting_reply" || state === "none" || state === "error";
}
// The terminal transition that must auto-close the panel: reached "done" with a
// created ticket key. Ideation's "done" is an end, not a restart-ready idle
// state — same terminal-state model the Start-Project wizard uses (SC-859).
export function shouldCloseIdeation(state, createdKey) {
    return state === "done" && typeof createdKey === "string" && createdKey.trim() !== "";
}
