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
export function queueIndex(queue) {
    return QUEUES.indexOf(queue);
}
export function isNextQueue(fromQueue, toQueue) {
    return queueIndex(toQueue) === queueIndex(fromQueue) + 1;
}
