// Pure predicates/builders for the Product-Backlog description-edit chat
// modal (SC-2873), kept free of DOM and Wails bindings so they can be unit-
// tested directly (mirrors board-detail.ts).
// descEditInputEnabled: the chat input/send are live whenever there is a
// session to type into. They stay live THROUGH a turn (SC-5033): a correction
// that occurs to you mid-answer must be typeable, and descEditSendDefers says
// what happens to it. "none" has no session yet and "applied" is terminal —
// Apply closes the dialog, so it is never a state the user sits in.
export function descEditInputEnabled(state) {
    return state !== "none" && state !== "applied";
}
// descEditApplyEnabled: Apply/Save is live only when a proposal exists, the
// session isn't mid-turn, and it hasn't already been applied this session —
// the disable-on-click guard mirrors the detail panel's decision buttons.
export function descEditApplyEnabled(state, proposal) {
    return !!proposal && proposal.trim() !== "" && state !== "thinking" && state !== "applied";
}
// descEditSendDefers: a message typed mid-turn is held, not sent. The daemon
// refuses a reply to a session that is not awaiting one
// (internal/daemon/descedit.go Reply), so sending now would surface an error
// on top of the answer still being written.
export function descEditSendDefers(state) {
    return state === "thinking";
}
// descEditHasUnappliedRewrite: leaving with one of these on screen destroys
// work the dialog itself labels "Proposed rewrite (unsaved)", so the dismiss
// path asks first. "applied" is not unapplied — Apply already landed it.
export function descEditHasUnappliedRewrite(state, proposal) {
    return !!proposal && proposal.trim() !== "" && state !== "applied";
}
// descEditStatusLine resolves what the line under the transcript says. It is
// the one place that knows a held message exists, because the transcript
// cannot show it: it has not been sent yet.
export function descEditStatusLine(state, error, queued) {
    if (state === "thinking") {
        return {
            text: queued ? "Thinking… — the message you typed sends when this answer lands." : "Thinking…",
            kind: "info",
        };
    }
    if (state === "error")
        return { text: error || "Description chat failed", kind: "error" };
    if (state === "applied")
        return { text: "Saved.", kind: "info" };
    return { text: "", kind: "none" };
}
// chatInputHeight: the grown height of the chat input, capped. One line to
// start, growing with the text, scrolling past the ceiling — the ceiling is
// six lines at THIS input's metrics (124px; the composer's rows="6" is ~112px
// because it sets no line-height, so the two are the same six lines and not
// the same pixels). Read from the element's own computed max-height so CSS
// stays the single source of it.
export function chatInputHeight(scrollHeight, maxPx) {
    if (!(maxPx > 0))
        return scrollHeight;
    return Math.min(scrollHeight, maxPx);
}
// descEditShouldDiscardOnClose: AC6 — closing the modal without Apply/Save
// must discard the pending session, so a later reopen of the same ticket
// never reattaches to a stale proposal or chat history. Only a live,
// un-applied session needs discarding: "applied" is already terminal (Apply
// itself ended the pending-proposal lifecycle, and reopening re-fetches the
// saved text from the tracker per AC5), and no sessionId means there is
// nothing running on the daemon side to discard.
export function descEditShouldDiscardOnClose(state, sessionId) {
    return !!sessionId && state !== "applied" && state !== "none";
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
export function draftNotice(state, hasDescription) {
    if (state === "failed") {
        return {
            kind: "failed",
            text: "The background draft failed — nothing was written. Ask for a rewrite below, or write the description yourself.",
        };
    }
    if (state === "drafting") {
        return { kind: "drafting", text: "A background draft is still being written…" };
    }
    if (!hasDescription) {
        return { kind: "none", text: "No draft has been written for this ticket yet." };
    }
    return { kind: "none", text: "" };
}
// descEditAllowedFor is the description editor's lane gate. A click opens it
// only on a Product-Backlog feature card; a promotion opens it on a card the
// board is still rendering in Ideas, because the labels have come off the
// ticket but no refetch has happened yet. Passing that fact in beats waiting
// for the refetch: the modal must open on the drop, not a poll later.
export function descEditAllowedFor(queue, bug = false, security = false, justPromoted = false) {
    if (bug || security)
        return false;
    return justPromoted || queue === "product";
}
