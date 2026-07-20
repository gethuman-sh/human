// The unified Deploy control builder. board.ts bootstraps against document at
// import time and cannot be unit-tested, so the DOM builder that both Deploy
// controls share lives here (touching document only inside the function) and the
// DOM-free view/selection logic lives in board-queue.ts — the same split as
// statsview / board-queue.
// buildDeployControl builds the single Deploy widget that supports BOTH halves
// of the interaction at once: dataset.drop makes it a drop target for one dragged
// card, and the click listener ships every deployable card. Unifying both halves
// behind one builder is the fix for 849 — the Features control previously had
// only the drop half (no click listener) and the Bugs control only the click
// half (no data-drop, so the drag hit-test never saw it).
export function buildDeployControl(view, opts) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = opts.className;
    // Drop half: the drag hit-test (dropTargetAt) finds any [data-drop] element,
    // and dropAllowed gates a single card here via isReadyToDeploy.
    btn.dataset.drop = "deploy";
    btn.disabled = view.disabled;
    btn.title = view.tooltip;
    btn.innerHTML = `<span class="deploy-zone-label">${view.label}</span>`;
    // Click half: ships every deployable card in the pane at once.
    btn.addEventListener("click", opts.onClick);
    return btn;
}
