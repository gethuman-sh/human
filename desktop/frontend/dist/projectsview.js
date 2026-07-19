// Projects Overview: shown instead of the board when no daemon is running,
// or when the user activates "Switch Project". Lists up to the 10
// most-recently-opened projects and lets the user open any directory
// containing a .humanconfig.yaml file — selecting one stops whatever
// daemon is running and starts the chosen project's daemon.
let bindings = null;
let onOpened = null;
let recents = [];
let opening = false;
let error = "";
function host() {
    return document.getElementById("projects-overview");
}
function escapeHtml(s) {
    return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}
function errMessage(err) {
    return err instanceof Error ? err.message : String(err);
}
// initProjectsView wires the module to the App bindings and to a callback
// invoked once a project's daemon is up and the board should be shown.
export function initProjectsView(getBindings, onProjectOpened) {
    bindings = getBindings;
    onOpened = onProjectOpened;
}
// showProjectsOverview (re)fetches the recent-projects list and renders.
// initialError seeds the banner (e.g. a failed auto-load reason).
export async function showProjectsOverview(initialError) {
    error = initialError || "";
    opening = false;
    const h = host();
    if (!h || !bindings)
        return;
    try {
        recents = (await bindings().RecentProjects()) ?? [];
    }
    catch {
        recents = [];
    }
    render();
}
function render() {
    const h = host();
    if (!h)
        return;
    h.innerHTML =
        `<div class="projects-overview-panel">` +
            `<h1 class="projects-title">Open a project</h1>` +
            (error ? `<div class="projects-error">${escapeHtml(error)}</div>` : "") +
            (recents.length
                ? `<div class="projects-recent-list">` +
                    recents
                        .map((p, i) => `<button class="projects-recent-item" data-i="${i}" type="button" ${opening ? "disabled" : ""}>` +
                        `<span class="projects-recent-name">${escapeHtml(p.name)}</span>` +
                        `<span class="projects-recent-path">${escapeHtml(p.dir)}</span>` +
                        `</button>`)
                        .join("") +
                    `</div>`
                : `<div class="projects-empty">No recent projects yet.</div>`) +
            `<div class="projects-open-row">` +
            `<input id="projects-path-input" class="projects-path-input" type="text" placeholder="/path/to/project" ${opening ? "disabled" : ""} />` +
            `<button id="projects-path-open" class="projects-path-open" type="button" ${opening ? "disabled" : ""}>Open</button>` +
            `<button id="projects-browse" class="projects-browse" type="button" ${opening ? "disabled" : ""}>Browse…</button>` +
            `</div>` +
            (opening ? `<div class="projects-opening">Starting project daemon…</div>` : "") +
            `</div>`;
    h.querySelectorAll(".projects-recent-item").forEach((btn) => {
        btn.addEventListener("click", () => void openDir(recents[Number(btn.dataset.i)]?.dir));
    });
    h.querySelector("#projects-path-open")?.addEventListener("click", () => {
        const input = document.getElementById("projects-path-input");
        if (input?.value.trim())
            void openDir(input.value.trim());
    });
    h.querySelector("#projects-browse")?.addEventListener("click", () => void browse());
}
async function browse() {
    if (!bindings)
        return;
    try {
        const dir = await bindings().BrowseForProjectDir();
        if (dir)
            void openDir(dir);
    }
    catch (err) {
        error = errMessage(err);
        render();
    }
}
async function openDir(dir) {
    if (!dir || !bindings || opening)
        return;
    opening = true;
    error = "";
    render();
    try {
        const project = await bindings().OpenProject(dir);
        opening = false;
        onOpened?.(project);
    }
    catch (err) {
        opening = false;
        error = errMessage(err);
        render();
    }
}
