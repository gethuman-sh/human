// Pure predicates/builders for the Product-Backlog description-edit chat
// modal (SC-2873), kept free of DOM and Wails bindings so they can be unit-
// tested directly (mirrors board-ideation.ts / board-detail.ts).
// descEditInputEnabled mirrors ideationInputEnabled: the chat input/send are
// live only while the session is idle and interactive. "applied" is terminal
// for THIS session (mirrors ideation's "done") — the user closes and reopens
// to start a fresh chat against the now-saved description.
export function descEditInputEnabled(state) {
    return state === "awaiting_reply" || state === "error";
}
// descEditApplyEnabled: Apply/Save is live only when a proposal exists, the
// session isn't mid-turn, and it hasn't already been applied this session —
// the disable-on-click guard mirrors the detail panel's decision buttons.
export function descEditApplyEnabled(state, proposal) {
    return !!proposal && proposal.trim() !== "" && state !== "thinking" && state !== "applied";
}
// buildDescriptionPreview resolves which text the left pane shows: the live
// proposal while one is pending review, otherwise the last-known-saved
// description. "applied" folds the proposal into the saved text (no longer a
// preview) — Apply/Save just landed it on the tracker.
export function buildDescriptionPreview(saved, proposal, state) {
    if (state === "applied") {
        return { text: proposal && proposal.trim() !== "" ? proposal : saved, isPreview: false };
    }
    if (proposal && proposal.trim() !== "") {
        return { text: proposal, isPreview: true };
    }
    return { text: saved, isPreview: false };
}
