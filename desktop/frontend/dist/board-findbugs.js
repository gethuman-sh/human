// Pure seam for the Bugs pane header so its Findbugs control is unit-testable
// without a DOM (mirrors board-detail.ts / board-queue.ts). renderBugs() injects
// the returned HTML and wires the click; this module owns only the markup.
// bugsHeaderHTML builds the Bugs column header. When a sweep is running it shows
// a spinner + "hunting…" label; otherwise it offers the Findbugs button that
// launches a project-wide sweep. The neutral "+" quick-add and the count always
// follow, so the two controls sit together to the right of the title.
export function bugsHeaderHTML(hunting, count) {
    const action = hunting
        ? `<span class="findbugs-hunting" title="A bug hunt is in progress…"><span class="spinner"></span> hunting…</span>`
        : `<button class="findbugs-btn" title="Sweep the project for bugs">Findbugs</button>`;
    return (`<div class="column-header"><span>Bugs</span>` +
        action +
        `<button class="add-card" title="File a bug">+</button>` +
        `<span class="column-count">${count}</span></div>`);
}
// securityHeaderHTML builds the Security half's header — the counterpart to
// bugsHeaderHTML. When a sweep is running it shows a spinner + "scanning…";
// otherwise it offers the Find Security button that launches the project-wide
// human-security scan. The neutral "+" quick-add and the count always follow.
export function securityHeaderHTML(hunting, count) {
    const action = hunting
        ? `<span class="findsecurity-hunting" title="A security scan is in progress…"><span class="spinner"></span> scanning…</span>`
        : `<button class="findsecurity-btn" title="Scan the project for vulnerabilities">Find Security</button>`;
    return (`<div class="column-header"><span>Security</span>` +
        action +
        `<button class="add-card" title="File a security issue">+</button>` +
        `<span class="column-count">${count}</span></div>`);
}
