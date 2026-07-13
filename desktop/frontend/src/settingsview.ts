// Settings view: renders the daemon's masked config snapshot (App.Settings)
// as a sidebar-of-sections + form cards, and writes single keys back through
// App.SaveSetting. The cached doc and save path are shared with the command
// palette, so both surfaces stay one data source with one write path.
//
// Secrets never appear here: vault references arrive verbatim (they are
// pointers, not secrets) and literal tokens arrive as a masked sentinel the
// daemon refuses to write back — secret inputs are therefore write-only.

import { toggleTheme } from "./fancy.js";

export interface SettingValue {
  path: string;
  section: string;
  sectionLabel: string;
  group: string;
  groupLabel: string;
  kind?: string;
  instance?: string;
  field: string;
  label: string;
  type: string;
  enum?: string[];
  value: unknown;
  masked?: boolean;
  secretRef?: boolean;
  restartRequired?: boolean;
  readOnly?: boolean;
  description?: string;
}

export interface SettingsDoc {
  dir: string;
  configFile: string;
  exists: boolean;
  values: SettingValue[];
  warnings?: string[];
}

export interface SettingsDaemonInfo {
  running: boolean;
  version?: string;
  addr?: string;
  pid?: number;
  projects?: string[];
}

export interface SettingsData {
  doc?: SettingsDoc;
  daemon: SettingsDaemonInfo;
  error?: string;
}

export interface SettingsBindings {
  Settings(): Promise<SettingsData>;
  SaveSetting(path: string, valueJSON: string): Promise<SettingsData>;
}

// The Appearance section is client-side only (theme lives in localStorage,
// toggled by fancy.ts) — it is appended after the doc-driven sections.
const APPEARANCE_SECTION = "appearance";

let bindings: (() => SettingsBindings) | null = null;
let data: SettingsData | null = null;
let activeSection = "";
// Wired by the command palette module; the sidebar search box is an entry
// point into the palette rather than a separate filter implementation.
let paletteOpener: (() => void) | null = null;

export function initSettingsView(getBindings: () => SettingsBindings): void {
  bindings = getBindings;
}

export function setPaletteOpener(open: () => void): void {
  paletteOpener = open;
}

// settingsIndex exposes the cached flattened values for the palette's search
// index. Empty until the view has loaded once (the palette refreshes first).
export function settingsIndex(): SettingValue[] {
  return data?.doc?.values ?? [];
}

// showSettings refreshes the snapshot and renders. Called on every view
// activation because the file can change on disk at any time (CLI, agents,
// editors).
export async function showSettings(): Promise<void> {
  if (!bindings) return;
  try {
    data = await bindings().Settings();
  } catch (err) {
    data = { daemon: { running: false }, error: String(err) };
  }
  if (!activeSection) activeSection = firstSection();
  render();
}

// saveSetting writes one key and refreshes the cached doc from the response
// (one round trip). Shared by the form rows and the palette editor. Throws
// with the daemon's message on rejection so callers can render it inline.
export async function saveSetting(path: string, value: unknown): Promise<SettingsData> {
  if (!bindings) throw new Error("settings bindings not ready");
  const result = await bindings().SaveSetting(path, JSON.stringify(value));
  if (result.error) throw new Error(result.error);
  data = result;
  return result;
}

function host(): HTMLElement | null {
  return document.getElementById("settings");
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function firstSection(): string {
  return data?.doc?.values[0]?.section ?? APPEARANCE_SECTION;
}

interface SectionEntry {
  key: string;
  label: string;
  count: number;
}

function sections(): SectionEntry[] {
  const out: SectionEntry[] = [];
  for (const v of data?.doc?.values ?? []) {
    const existing = out.find((s) => s.key === v.section);
    if (existing) existing.count++;
    else out.push({ key: v.section, label: v.sectionLabel, count: 1 });
  }
  out.push({ key: APPEARANCE_SECTION, label: "Appearance", count: 1 });
  return out;
}

// Instance cards: values grouped by group + instance address so each tracker
// instance (or singleton section) renders as one card.
interface InstanceCard {
  groupLabel: string;
  kind?: string;
  instance?: string;
  values: SettingValue[];
}

function cardsFor(section: string): InstanceCard[] {
  const cards: InstanceCard[] = [];
  const byAddress = new Map<string, InstanceCard>();
  for (const v of data?.doc?.values ?? []) {
    if (v.section !== section) continue;
    const address = v.path.slice(0, v.path.length - v.field.length).replace(/\.$/, "");
    let card = byAddress.get(address);
    if (!card) {
      card = { groupLabel: v.groupLabel, kind: v.kind, instance: v.instance, values: [] };
      byAddress.set(address, card);
      cards.push(card);
    }
    card.values.push(v);
  }
  return cards;
}

function render(): void {
  const h = host();
  if (!h) return;
  if (!data) {
    h.innerHTML = `<div class="settings-empty">Loading settings…</div>`;
    return;
  }
  if (!data.doc) {
    h.innerHTML =
      `<div class="settings-banner error">${escapeHtml(data.error ?? "Settings unavailable")}</div>`;
    return;
  }
  h.innerHTML = renderSidebar() + renderContent();
  wireSidebar(h);
  wireRows(h);
  wireAppearance(h);
}

function renderSidebar(): string {
  const doc = data?.doc;
  const items = sections()
    .map(
      (s) =>
        `<button class="settings-sec${s.key === activeSection ? " active" : ""}" data-sec="${escapeHtml(s.key)}" type="button">` +
        `<span>${escapeHtml(s.label)}</span>` +
        (s.key === APPEARANCE_SECTION ? "" : `<span class="settings-sec-count">${s.count}</span>`) +
        `</button>`,
    )
    .join("");
  const file = doc?.exists
    ? escapeHtml(doc.configFile)
    : "no config file yet — first save creates .humanconfig.yaml";
  return (
    `<div class="settings-side">` +
    `<button class="settings-search" type="button" title="Search all settings (Ctrl+,)">` +
    `<span>⌕ Search settings…</span><span class="settings-kbd">Ctrl ,</span>` +
    `</button>` +
    items +
    `<div class="settings-file" title="${file}">${file}</div>` +
    `</div>`
  );
}

function renderContent(): string {
  const doc = data?.doc;
  let body = "";
  if (activeSection === APPEARANCE_SECTION) {
    body = renderAppearance();
  } else {
    body = cardsFor(activeSection).map(renderCard).join("") || `<div class="settings-empty">Nothing configured in this section yet — edit .humanconfig.yaml to add instances (adding from the UI is a planned follow-up).</div>`;
    if (activeSection === "daemon") body = renderDaemonHeader() + body;
  }
  const warnings = (doc?.warnings ?? [])
    .map((w) => `<div class="settings-banner warn">${escapeHtml(w)}</div>`)
    .join("");
  return `<div class="settings-content">${warnings}${body}</div>`;
}

function renderDaemonHeader(): string {
  const d = data?.daemon;
  if (!d) return "";
  const dot = d.running ? "running" : "stopped";
  const meta = [d.version, d.addr, d.pid ? `pid ${d.pid}` : ""].filter(Boolean).join(" · ");
  return (
    `<div class="settings-card settings-daemon">` +
    `<div class="settings-card-head"><span class="settings-dot ${dot}"></span>` +
    `<span class="settings-card-name">Daemon ${d.running ? "running" : "not running"}</span>` +
    `<span class="settings-card-meta">${escapeHtml(meta)}</span></div>` +
    (d.projects?.length
      ? `<div class="settings-card-meta">projects: ${escapeHtml(d.projects.join(", "))}</div>`
      : "") +
    `</div>`
  );
}

function renderAppearance(): string {
  return (
    `<div class="settings-card">` +
    `<div class="settings-card-head"><span class="settings-card-name">Theme</span></div>` +
    `<div class="settings-row"><span class="settings-label">Fancy theme</span>` +
    `<button id="settings-theme-toggle" class="settings-btn" type="button">Toggle theme</button>` +
    `<span class="settings-hint">F8 toggles it anywhere — stored locally, not in .humanconfig</span>` +
    `</div></div>`
  );
}

function renderCard(card: InstanceCard): string {
  const head =
    `<div class="settings-card-head">` +
    (card.kind ? `<span class="settings-kind">${escapeHtml(card.kind)}</span>` : "") +
    `<span class="settings-card-name">${escapeHtml(card.instance || card.groupLabel)}</span>` +
    (card.instance ? `<span class="settings-card-meta">${escapeHtml(card.groupLabel)}</span>` : "") +
    `</div>`;
  return `<div class="settings-card">${head}${card.values.map(renderRow).join("")}</div>`;
}

function renderRow(v: SettingValue): string {
  const badge = v.restartRequired
    ? `<span class="settings-restart" title="Takes effect after daemon restart">restart</span>`
    : "";
  const readOnlyNote = v.readOnly
    ? `<span class="settings-hint">duplicate name — edit the file directly</span>`
    : "";
  return (
    `<div class="settings-row" data-row="${escapeHtml(v.path)}">` +
    `<span class="settings-label" title="${escapeHtml(v.description ?? "")}">${escapeHtml(v.label)}</span>` +
    renderEditor(v) +
    badge +
    readOnlyNote +
    `<span class="settings-flash" aria-hidden="true">✓</span>` +
    `<div class="settings-error hidden"></div>` +
    `</div>`
  );
}

function renderEditor(v: SettingValue): string {
  const dis = v.readOnly ? " disabled" : "";
  const path = escapeHtml(v.path);
  switch (v.type) {
    case "bool":
      return `<input type="checkbox" class="settings-input" data-path="${path}" data-type="${v.type}"${(v.value as boolean) ? " checked" : ""}${dis} />`;
    case "enum": {
      const opts = ["", ...(v.enum ?? [])]
        .map((o) => `<option value="${escapeHtml(o)}"${o === v.value ? " selected" : ""}>${escapeHtml(o || "—")}</option>`)
        .join("");
      return `<select class="settings-input" data-path="${path}" data-type="${v.type}"${dis}>${opts}</select>`;
    }
    case "stringlist":
    case "intlist": {
      const items = Array.isArray(v.value) ? (v.value as unknown[]).map(String) : [];
      return `<textarea class="settings-input settings-list" rows="${Math.max(1, items.length)}" data-path="${path}" data-type="${v.type}" placeholder="one per line"${dis}>${escapeHtml(items.join("\n"))}</textarea>`;
    }
    case "int":
      return `<input type="text" inputmode="numeric" class="settings-input" data-path="${path}" data-type="${v.type}" value="${escapeHtml(String(v.value ?? ""))}"${dis} />`;
    case "secret": {
      if (v.secretRef) {
        return `<input type="text" class="settings-input settings-vaultref" data-path="${path}" data-type="${v.type}" value="${escapeHtml(String(v.value))}"${dis} />`;
      }
      const placeholder = v.masked ? "•••••• (unchanged — type to replace)" : "empty — paste token or 1pw:// reference";
      return `<input type="password" class="settings-input" data-path="${path}" data-type="${v.type}" data-writeonly="1" placeholder="${placeholder}"${dis} />`;
    }
    default:
      return `<input type="text" class="settings-input" data-path="${path}" data-type="${v.type}" value="${escapeHtml(String(v.value ?? ""))}"${dis} />`;
  }
}

function wireSidebar(h: HTMLElement): void {
  h.querySelectorAll<HTMLButtonElement>(".settings-sec").forEach((btn) => {
    btn.addEventListener("click", () => {
      activeSection = btn.dataset.sec ?? firstSection();
      render();
    });
  });
  h.querySelector(".settings-search")?.addEventListener("click", () => {
    paletteOpener?.();
  });
}

function wireAppearance(h: HTMLElement): void {
  h.querySelector("#settings-theme-toggle")?.addEventListener("click", () => toggleTheme());
}

function wireRows(h: HTMLElement): void {
  h.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(".settings-input").forEach((input) => {
    if (input.disabled) return;
    const commit = (): void => void commitRow(input);
    input.addEventListener("blur", commit);
    input.addEventListener("keydown", (e: Event) => {
      const ke = e as KeyboardEvent;
      if (ke.key === "Enter" && input.tagName !== "TEXTAREA") {
        ke.preventDefault();
        input.blur();
      }
    });
    if (input.tagName === "SELECT" || (input as HTMLInputElement).type === "checkbox") {
      input.addEventListener("change", commit);
    }
  });
}

// parseInput turns an editor's raw state into the typed value SaveSetting
// expects, or throws a user-facing message for client-side shape errors
// (the daemon re-validates authoritatively).
export function parseInput(type: string, raw: string, checked?: boolean): unknown {
  switch (type) {
    case "bool":
      return checked ?? false;
    case "stringlist":
      return lines(raw);
    case "intlist":
      return lines(raw).map(parseIntStrict);
    case "int":
      return raw.trim() === "" ? 0 : parseIntStrict(raw.trim());
    default:
      return raw;
  }
}

function lines(raw: string): string[] {
  return raw
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "");
}

function parseIntStrict(s: string): number {
  if (!/^-?\d+$/.test(s)) throw new Error(`not an integer: ${s}`);
  return Number(s);
}

async function commitRow(input: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement): Promise<void> {
  const path = input.dataset.path ?? "";
  const type = input.dataset.type ?? "string";
  const original = settingsIndex().find((v) => v.path === path);
  if (!original) return;

  // Write-only secret inputs: an untouched (empty) field means "unchanged".
  if (input.dataset.writeonly === "1" && input.value === "") return;

  let parsed: unknown;
  try {
    parsed = parseInput(type, input.value, (input as HTMLInputElement).checked);
  } catch (err) {
    showRowError(path, String(err instanceof Error ? err.message : err));
    return;
  }
  if (!input.dataset.writeonly && JSON.stringify(parsed) === JSON.stringify(original.value)) {
    clearRowError(path);
    return;
  }
  try {
    await saveSetting(path, parsed);
    // Re-render so masked state, warnings, and the palette index reflect the
    // new snapshot; the blur already happened so focus loss is moot. The ✓
    // flash targets the fresh DOM, so it must come after the render.
    const content = host()?.querySelector(".settings-content");
    const scroll = content ? content.scrollTop : 0;
    render();
    const fresh = host()?.querySelector(".settings-content");
    if (fresh) fresh.scrollTop = scroll;
    flashRow(path);
  } catch (err) {
    showRowError(path, String(err instanceof Error ? err.message : err));
  }
}

function rowEl(path: string): HTMLElement | null {
  return host()?.querySelector(`[data-row="${CSS.escape(path)}"]`) ?? null;
}

function showRowError(path: string, message: string): void {
  const row = rowEl(path);
  const err = row?.querySelector(".settings-error");
  if (err) {
    err.textContent = message;
    err.classList.remove("hidden");
  }
}

function clearRowError(path: string): void {
  rowEl(path)?.querySelector(".settings-error")?.classList.add("hidden");
}

function flashRow(path: string): void {
  const flash = rowEl(path)?.querySelector(".settings-flash");
  flash?.classList.add("show");
  setTimeout(() => flash?.classList.remove("show"), 1500);
}
