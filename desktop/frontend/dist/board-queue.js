// Pure column-placement and drop-gate logic for the workflow board, kept free
// of DOM and Wails bindings so it can be unit-tested directly (the rest of
// board.ts bootstraps against document/window.go at import time and cannot).
export const QUEUES = ["ideas", "product", "engineering", "building", "deploy"];
// Wire stage launched by dropping onto a queue from its predecessor.
export const QUEUE_TRANSITION_TO = {
    engineering: "planning",
    building: "implementation",
};
export function verdictFailed(verdict) {
    return (verdict ?? "").trim().toLowerCase().startsWith("fail");
}
// queueOf maps (stage, state) onto the column that is true of the card. Running
// and failed cards render in their DESTINATION lane, not their origin queue:
// planning lives in Engineering, implementation/verification in Code — the card
// stays visibly where the user dropped it while its stage runs.
export function queueOf(card) {
    switch (card.stage) {
        case "ideas":
            return "ideas";
        case "backlog":
            return "product";
        case "planning":
            return "engineering";
        case "implementation":
            return "building";
        case "verification":
            return card.state === "done" && !verdictFailed(card.verdict) && !!card.branch ? "deploy" : "building";
        case "done":
            return "deploy";
        default:
            return "product";
    }
}
export function isReworkable(card) {
    return card.stage === "verification" && card.state === "done" && (verdictFailed(card.verdict) || !card.branch);
}
// Live badge text while a stage runs; builds and their chained reviews both
// live in the Code lane, deploys in Ready to Deploy.
export const RUNNING_LABELS = {
    planning: "planning…",
    implementation: "building…",
    verification: "reviewing…",
    done: "deploying…",
};
// badgeInfo classifies a card's live state into a badge descriptor, or null
// when the card rests and needs none — its queue position IS the statement of
// completion. A review that found problems is a WARNING, not a stage failure:
// the work exists, it just may not advance until a rebuild passes.
export function badgeInfo(card) {
    if (card.state === "running") {
        return {
            cls: "running",
            text: RUNNING_LABELS[card.stage] ?? "working…",
            title: "Agent running",
            spinner: true,
        };
    }
    if (card.state === "failed")
        return { cls: "failed", text: "✕", title: "Stage failed" };
    if (card.state === "resolved") {
        if (card.stage === "planning") {
            // The planner verified the ticket's work is already merged, so there is
            // nothing left to plan: a successful terminal outcome, never red, never
            // deployable — the right resolution is Done, not re-planning (ticket 454).
            return { cls: "resolved", text: "already shipped", title: "Work already merged — nothing left to plan" };
        }
        // An autofix run whose triage concluded no fix is warranted (not-a-bug or
        // undetermined): a successful terminal outcome, never red, never deployable
        // (ticket 405).
        return { cls: "resolved", text: "no fix needed", title: "Triage concluded no fix is warranted" };
    }
    // An open decision block outranks the generic review warning: the review
    // deliberately handed the human a fork, and the actionable statement is
    // "pick one", not "problems found" (ticket 372/534).
    if (card.options && card.options.length > 0) {
        return {
            cls: "decision",
            text: "decision needed",
            title: `The review offers ${card.options.length} ways forward — open the card to choose`,
        };
    }
    if (card.stage === "verification" && card.state === "done" && verdictFailed(card.verdict)) {
        return { cls: "warning", text: "⚠ review found problems", title: `Review verdict: ${card.verdict ?? ""}` };
    }
    if (card.stage === "verification" && card.state === "done" && !card.branch) {
        // A passed review with no recorded branch has nothing to ship — deploying
        // it can only fail, so it must read as needing a rebuild, never as ready.
        return {
            cls: "warning",
            text: "⚠ no branch recorded",
            title: "Review passed but no branch was recorded on the handoff — drop it on the build stage to rebuild",
        };
    }
    // SC-429: fix complete, review not started is a durable hand-off state, not a
    // sub-second transient — it must read as a neutral wait, never render blank.
    if (card.stage === "implementation" && card.state === "done") {
        return { cls: "await", text: "awaiting review…", title: "Fix complete — waiting for review to start" };
    }
    if (card.stage === "done" && card.state === "done") {
        return { cls: "done", text: "deployed", title: "Merged and shipped" };
    }
    return null;
}
// planReady reports a planning card whose plan is complete — the only planning
// state permitted to advance forward into Code. A running or failed planning
// card in Engineering must NOT launch implementation on an unplanned ticket.
export function planReady(card) {
    return card.stage === "planning" && card.state === "done";
}
// forwardDropAllowed is the queue-transition predicate: forward-adjacency, plus
// the Code rework re-drop, plus the plan-ready gate on advancing OUT of
// Engineering. DOM/Docker gating stays in board.ts's dropAllowed.
export function forwardDropAllowed(card, toQueue) {
    if (toQueue === "building" && isReworkable(card))
        return true;
    const from = queueOf(card);
    if (!isNextQueue(from, toQueue))
        return false;
    // Engineering -> Code may only launch implementation once the plan is ready.
    if (from === "engineering" && toQueue === "building")
        return planReady(card);
    return true;
}
// sortByHandOrder sorts cards by a saved hand-sorted key list: listed cards
// first in list order, unlisted cards after in their existing (fetch) order.
// In place, relying on Array.prototype.sort's stability for the tail.
export function sortByHandOrder(cards, order) {
    if (!order || order.length === 0)
        return cards;
    const pos = new Map(order.map((k, i) => [k, i]));
    return cards.sort((a, b) => (pos.get(a.key) ?? Number.MAX_SAFE_INTEGER) - (pos.get(b.key) ?? Number.MAX_SAFE_INTEGER));
}
// insertKeyAt rebuilds a column's hand-sorted key list after a same-column
// drop: the dragged key lands before the first resting card whose vertical
// midpoint is below the drop point, or last when the drop was below them all.
export function insertKeyAt(restingKeys, midpoints, dragged, dropY) {
    const keys = [];
    let inserted = false;
    restingKeys.forEach((k, i) => {
        if (!inserted && dropY < midpoints[i]) {
            keys.push(dragged);
            inserted = true;
        }
        keys.push(k);
    });
    if (!inserted)
        keys.push(dragged);
    return keys;
}
export function queueIndex(queue) {
    return QUEUES.indexOf(queue);
}
export function isNextQueue(fromQueue, toQueue) {
    return queueIndex(toQueue) === queueIndex(fromQueue) + 1;
}
