// Mockups view: navigates the tree of mockup groups the /human-mockups skill
// generates (mockups/<slug>/index.json in each registered project) and shows a
// group's options in an iframe. Files are served live from disk by the Go
// asset-server middleware at /mockups/<slug>/<file>, so a freshly generated
// group appears the next time the view loads — nothing is embedded.
//
// Beyond the flat first-round list, a group can spawn child "variation" groups
// (variants of one of its mockups, honoring free-text instructions) at any
// depth. The viewer renders a breadcrumb up to the root, the child groups below
// the option tabs, a host-side "Create variations" control, winner selection on
// a leaf option, and pruning of dead branches. "View mocks" always opens the
// root group; deeper groups are reached by navigating down from there.
let bindings = null;
let sets = [];
let active = null;
let activeOption = 0;
// pendingSlug deep-links the next view activation to one set (the board's
// "View mocks"). Consumed by showMockups so selectView("mockups") stays the
// single fetch+render path instead of racing a second navigation call.
let pendingSlug = null;
// chosenSlug/chosenFile track the ticket's current winner so the viewer can
// highlight the root→winner path and mark the chosen option. Seeded from the
// card when "View mocks" opens and updated locally when a winner is (un)set.
let chosenSlug = "";
let chosenFile = "";
// pendingVariationParent is the slug of a group whose variation creation was
// just requested; it shows an optimistic "Creating variations…" chip until the
// next fetch surfaces the real (validated) child group.
let pendingVariationParent = null;
// variationPanelOpen tracks whether the inline free-text variation form is
// expanded on the active option's toolbar.
let variationPanelOpen = false;
// setPendingMockupSlug asks the next showMockups() to open this set directly.
export function setPendingMockupSlug(slug) {
    pendingSlug = slug;
}
// setChosenMockup seeds the viewer with the ticket's current winner (group slug
// + option file), so the highlight is correct on first render. Empty clears it.
export function setChosenMockup(slug, file) {
    chosenSlug = slug;
    chosenFile = file;
}
function host() {
    return document.getElementById("mockups");
}
function escapeHtml(s) {
    return s.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");
}
// childrenOf returns the groups directly varied from slug, oldest first so the
// chip order is stable across refreshes.
function childrenOf(slug) {
    return sets
        .filter((s) => s.parent === slug)
        .sort((a, b) => a.created.localeCompare(b.created));
}
// setBySlug resolves a group by slug from the fetched sets.
function setBySlug(slug) {
    return sets.find((s) => s.slug === slug) ?? null;
}
// pathToRoot walks parent links from set up to its root, returning the chain
// root-first. Orphans (parent not found) and cycles stop the walk, so an
// unresolvable parent simply attaches the group as a local root — navigation
// never breaks.
function pathToRoot(set) {
    const chain = [];
    const seen = new Set();
    let cur = set;
    while (cur && !seen.has(cur.slug)) {
        seen.add(cur.slug);
        chain.unshift(cur);
        cur = cur.parent ? setBySlug(cur.parent) : null;
    }
    return chain;
}
// rootFor returns the root group of set's tree (the topmost reachable ancestor).
function rootFor(set) {
    const chain = pathToRoot(set);
    return chain[0] ?? set;
}
function renderList() {
    const h = host();
    if (!h)
        return;
    // Only root groups belong in the top-level list; variation groups are reached
    // by navigating down from their root.
    const roots = sets.filter((s) => !s.parent || !setBySlug(s.parent));
    if (roots.length === 0) {
        h.innerHTML =
            `<div class="mockups-header">Mockups</div>` +
                `<div class="mockups-empty">No mockup sets found. Generate one with the ` +
                `<code>/human-mockups &lt;feature&gt;</code> skill — it writes ` +
                `<code>mockups/&lt;feature&gt;/</code> with an <code>index.json</code> this view picks up. ` +
                `From any mockup you can then spawn variation groups and mark a winner.</div>`;
        return;
    }
    h.innerHTML =
        `<div class="mockups-header">Mockups</div>` +
            `<div class="mockups-grid">` +
            roots
                .map((s) => {
                const i = sets.indexOf(s);
                const kids = childrenOf(s.slug).length;
                const kidNote = kids > 0 ? ` · ${kids} variation group${kids === 1 ? "" : "s"}` : "";
                return (`<button class="mockup-card" data-i="${i}" type="button">` +
                    `<span class="mockup-card-feature">${escapeHtml(s.feature)}</span>` +
                    `<span class="mockup-card-meta">${escapeHtml(s.project)} · ${escapeHtml(s.created)} · ${s.options.length} options${kidNote}</span>` +
                    `<span class="mockup-card-options">${s.options.map((o) => escapeHtml(o.name)).join(" · ")}</span>` +
                    `</button>`);
            })
                .join("") +
            `</div>`;
    h.querySelectorAll(".mockup-card").forEach((card) => {
        card.addEventListener("click", () => {
            active = sets[Number(card.dataset.i)] ?? null;
            activeOption = 0;
            variationPanelOpen = false;
            renderDetail();
        });
    });
}
// crumbLabel is the short breadcrumb text for a group: its feature name for a
// root, else the change instructions (trimmed) that produced the variation.
function crumbLabel(set) {
    if (!set.parent)
        return set.feature;
    const instr = (set.instructions ?? "").trim();
    return instr.length > 0 ? instr : "variation";
}
function renderDetail() {
    const h = host();
    if (!h || !active)
        return;
    const set = active;
    const opt = set.options[activeOption] ?? set.options[0];
    const ticket = set.ticket ?? "";
    const canManage = ticket !== "";
    const isRoot = !set.parent || !setBySlug(set.parent);
    const winnerHere = chosenSlug === set.slug;
    // The chosen group's ancestry, so every crumb on the winning path highlights.
    const winner = chosenSlug ? setBySlug(chosenSlug) : null;
    const winnerPath = winner ? new Set(pathToRoot(winner).map((s) => s.slug)) : new Set();
    const crumbs = pathToRoot(set)
        .map((s, idx, arr) => {
        const last = idx === arr.length - 1;
        const onPath = winnerPath.has(s.slug);
        const cls = `mockup-crumb${last ? " current" : ""}${onPath ? " on-winner-path" : ""}`;
        return (`<button class="${cls}" data-slug="${escapeHtml(s.slug)}" type="button"${last ? " disabled" : ""}>` +
            `${escapeHtml(crumbLabel(s))}</button>`);
    })
        .join(`<span class="mockup-crumb-sep">›</span>`);
    const tabs = set.options
        .map((o, i) => {
        const chosen = winnerHere && chosenFile === o.file;
        const cls = `mockup-tab${i === activeOption ? " active" : ""}${chosen ? " chosen" : ""}`;
        const mark = chosen ? "✓ " : "";
        return (`<button class="${cls}" data-i="${i}" title="${escapeHtml(o.description)}" type="button">` +
            `${mark}${o.n} · ${escapeHtml(o.name)}</button>`);
    })
        .join("");
    // Child variation groups, plus an optimistic chip while one is being created.
    const kids = childrenOf(set.slug);
    const kidChips = kids
        .map((c) => {
        const onPath = winnerPath.has(c.slug);
        const label = crumbLabel(c);
        return (`<button class="mockup-child-chip${onPath ? " on-winner-path" : ""}" data-slug="${escapeHtml(c.slug)}" type="button">` +
            `${c.options.length} variation${c.options.length === 1 ? "" : "s"}: ${escapeHtml(label)}</button>`);
    })
        .join("");
    const creatingChip = pendingVariationParent === set.slug
        ? `<span class="mockup-child-chip creating">Creating variations…</span>`
        : "";
    const childrenBlock = kidChips || creatingChip
        ? `<div class="mockup-children"><span class="mockup-children-label">Variation groups:</span>${kidChips}${creatingChip}</div>`
        : "";
    // Host-side controls for the active option (only when the group is
    // ticket-linked; ad-hoc sets stay read-only).
    const winBtn = winnerHere && chosenFile === opt.file
        ? `<button class="mockup-ctl chosen" data-act="unchoose" type="button">✓ Chosen design — clear</button>`
        : `<button class="mockup-ctl" data-act="choose" type="button">Mark as winner</button>`;
    const varBtn = `<button class="mockup-ctl" data-act="vary" type="button">Create variations</button>`;
    const pruneBtn = isRoot
        ? ""
        : `<button class="mockup-ctl danger" data-act="prune" type="button">Prune branch</button>`;
    const controls = canManage
        ? `<div class="mockup-controls">${varBtn}${winBtn}${pruneBtn}</div>`
        : "";
    const varPanel = canManage && variationPanelOpen
        ? `<div class="mockup-vary-panel">` +
            `<textarea class="mockup-vary-input" rows="3" placeholder="Describe what to add or change in this mockup…"></textarea>` +
            `<div class="mockup-vary-actions">` +
            `<button class="mockup-ctl" data-act="vary-submit" type="button">Create</button>` +
            `<button class="mockup-ctl" data-act="vary-cancel" type="button">Cancel</button>` +
            `</div></div>`
        : "";
    h.innerHTML =
        `<div class="mockups-toolbar">` +
            `<button class="mockup-back" type="button">‹ All mockups</button>` +
            `<span class="mockups-title">${escapeHtml(set.feature)}</span>` +
            `<span class="mockups-toolbar-meta">${escapeHtml(set.project)} · ${escapeHtml(set.created)}</span>` +
            `</div>` +
            `<div class="mockup-breadcrumb">${crumbs}</div>` +
            `<div class="mockup-tabs">${tabs}</div>` +
            controls +
            varPanel +
            childrenBlock +
            `<iframe class="mockup-frame" src="/mockups/${encodeURIComponent(set.slug)}/${encodeURIComponent(opt.file)}" ` +
            `title="${escapeHtml(opt.name)}"></iframe>`;
    h.querySelector(".mockup-back")?.addEventListener("click", () => {
        active = null;
        variationPanelOpen = false;
        renderList();
    });
    h.querySelectorAll(".mockup-tab").forEach((tab) => {
        tab.addEventListener("click", () => {
            activeOption = Number(tab.dataset.i) || 0;
            variationPanelOpen = false;
            renderDetail();
        });
    });
    h.querySelectorAll(".mockup-crumb").forEach((crumb) => {
        crumb.addEventListener("click", () => {
            const target = setBySlug(crumb.dataset.slug ?? "");
            if (target) {
                active = target;
                activeOption = 0;
                variationPanelOpen = false;
                renderDetail();
            }
        });
    });
    h.querySelectorAll(".mockup-child-chip[data-slug]").forEach((chip) => {
        chip.addEventListener("click", () => {
            const target = setBySlug(chip.dataset.slug ?? "");
            if (target) {
                active = target;
                activeOption = 0;
                variationPanelOpen = false;
                renderDetail();
            }
        });
    });
    h.querySelectorAll(".mockup-ctl").forEach((ctl) => {
        ctl.addEventListener("click", () => void handleControl(ctl.dataset.act ?? "", set, opt, ticket, h));
    });
}
// handleControl dispatches the host-side toolbar actions for the active option.
async function handleControl(act, set, opt, ticket, h) {
    if (!bindings || ticket === "")
        return;
    const b = bindings();
    switch (act) {
        case "vary":
            variationPanelOpen = true;
            renderDetail();
            break;
        case "vary-cancel":
            variationPanelOpen = false;
            renderDetail();
            break;
        case "vary-submit": {
            const input = h.querySelector(".mockup-vary-input");
            const instructions = (input?.value ?? "").trim();
            if (instructions === "") {
                input?.focus();
                return;
            }
            try {
                await b.CreateVariations(ticket, set.feature, set.slug, opt.file, instructions);
                pendingVariationParent = set.slug;
            }
            catch {
                // Leave the panel open on failure so the text is not lost.
                return;
            }
            variationPanelOpen = false;
            await showMockups();
            break;
        }
        case "choose":
            try {
                await b.ChooseMockup(ticket, set.slug, opt.file);
                chosenSlug = set.slug;
                chosenFile = opt.file;
            }
            catch {
                return;
            }
            renderDetail();
            break;
        case "unchoose":
            try {
                await b.ChooseMockup(ticket, "", "");
                chosenSlug = "";
                chosenFile = "";
            }
            catch {
                return;
            }
            renderDetail();
            break;
        case "prune": {
            if (!window.confirm("Prune this variation branch? It is archived and removed from navigation."))
                return;
            const parentSlug = set.parent ?? "";
            try {
                await b.PruneMockup(ticket, set.slug);
            }
            catch {
                return;
            }
            // Navigate to the parent (or the list) after pruning the current branch.
            const parent = parentSlug ? setBySlug(parentSlug) : null;
            active = parent;
            variationPanelOpen = false;
            await showMockups();
            break;
        }
        default:
            break;
    }
}
// showMockups refreshes the set list from disk and renders. Called on every
// view activation so newly generated sets appear without restarting the app.
export async function showMockups() {
    const h = host();
    if (!h || !bindings)
        return;
    try {
        sets = (await bindings().MockupSets()) ?? [];
    }
    catch {
        sets = [];
    }
    // Clear the optimistic chip once the real child group has materialized.
    if (pendingVariationParent && childrenOf(pendingVariationParent).length > 0) {
        pendingVariationParent = null;
    }
    // A deep-link wins over whatever was open. "View mocks" passes the root slug,
    // but resolve upward defensively so a deeper slug still opens the root group.
    if (pendingSlug) {
        const found = setBySlug(pendingSlug);
        active = found ? rootFor(found) : null;
        activeOption = 0;
        pendingSlug = null;
    }
    else if (active) {
        const slug = active.slug;
        active = setBySlug(slug);
    }
    if (active)
        renderDetail();
    else
        renderList();
}
export function initMockupsView(getBindings) {
    bindings = getBindings;
}
