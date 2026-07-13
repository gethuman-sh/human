// Settings command palette: Ctrl+, (or the settings sidebar's search box)
// opens an overlay that fuzzy-matches every .humanconfig leaf by dotted path,
// label, and description, shows current values inline, and edits the selected
// key in place. It reuses the settings view's cached snapshot and save path —
// one data source, one write path — and works over any active view.

import type { SettingsData, SettingValue } from "./settingsview.js";
import { parseInput } from "./settingsview.js";

export interface PaletteDeps {
  index(): SettingValue[];
  refresh(): Promise<void>;
  save(path: string, value: unknown): Promise<SettingsData>;
}

const MAX_HITS = 50;

let deps: PaletteDeps | null = null;
let overlay: HTMLElement | null = null;
let query = "";
let selected = 0;
let editing = false;

// isPaletteChord matches Ctrl/Cmd+, — the platform-conventional settings
// shortcut. Deliberately no editable-target guard: settings should open even
// while a form field has focus (IDE convention), unlike letter chords.
export function isPaletteChord(e: KeyboardEvent): boolean {
  return (e.ctrlKey || e.metaKey) && e.key === ",";
}

export function initPalette(paletteDeps: PaletteDeps): void {
  deps = paletteDeps;
}

export function openPalette(prefill?: string): void {
  if (!deps) return;
  if (overlay) closePalette();
  query = prefill ?? "";
  selected = 0;
  editing = false;
  buildOverlay();
  // A cold open (settings view never visited) has no index yet — load it,
  // then re-render the hit list in place.
  if (deps.index().length === 0) {
    void deps.refresh().then(() => renderHits());
  }
}

function closePalette(): void {
  overlay?.remove();
  overlay = null;
}

interface Hit {
  value: SettingValue;
  score: number;
  positions: number[]; // matched char indexes into value.path, for <mark>
}

// score is a dependency-free subsequence match: all query chars must appear
// in order; contiguous runs and segment starts (after '.') score higher, and
// path matches outrank label/description-only matches.
function scoreAgainst(text: string, q: string): { score: number; positions: number[] } | null {
  const lower = text.toLowerCase();
  const needle = q.toLowerCase();
  const positions: number[] = [];
  let score = 0;
  let ti = 0;
  for (const ch of needle) {
    const found = lower.indexOf(ch, ti);
    if (found < 0) return null;
    const prev = positions[positions.length - 1];
    if (prev !== undefined && found === prev + 1) score += 3; // contiguous run
    if (found === 0 || lower[found - 1] === "." || lower[found - 1] === " ") score += 2;
    score += 1;
    positions.push(found);
    ti = found + 1;
  }
  return { score, positions };
}

function hits(): Hit[] {
  const values = deps?.index() ?? [];
  if (query.trim() === "") {
    return values.slice(0, MAX_HITS).map((v) => ({ value: v, score: 0, positions: [] }));
  }
  const out: Hit[] = [];
  for (const v of values) {
    const onPath = scoreAgainst(v.path, query);
    if (onPath) {
      out.push({ value: v, score: onPath.score + 10, positions: onPath.positions });
      continue;
    }
    const onText = scoreAgainst(`${v.label} ${v.description ?? ""}`, query);
    if (onText) out.push({ value: v, score: onText.score, positions: [] });
  }
  out.sort((a, b) => b.score - a.score);
  return out.slice(0, MAX_HITS);
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function markPath(path: string, positions: number[]): string {
  if (positions.length === 0) return escapeHtml(path);
  let out = "";
  const marked = new Set(positions);
  for (let i = 0; i < path.length; i++) {
    const ch = escapeHtml(path[i]);
    out += marked.has(i) ? `<mark>${ch}</mark>` : ch;
  }
  return out;
}

function displayValue(v: SettingValue): string {
  if (v.masked) return "••••••";
  if (Array.isArray(v.value)) return (v.value as unknown[]).join(", ");
  return String(v.value ?? "");
}

function buildOverlay(): void {
  overlay = document.createElement("div");
  overlay.className = "modal-overlay palette-overlay";
  overlay.innerHTML =
    `<div class="palette">` +
    `<div class="palette-in"><span class="palette-glyph">⌕</span>` +
    `<input id="palette-input" type="text" autocomplete="off" spellcheck="false" placeholder="Search settings…" value="${escapeHtml(query)}" />` +
    `<span class="palette-scope">settings</span></div>` +
    `<div id="palette-hits" class="palette-hits"></div>` +
    `<div class="palette-foot"><span><b>↑↓</b> select</span><span><b>↵</b> edit</span><span><b>esc</b> close</span>` +
    `<span id="palette-count" class="palette-count"></span></div>` +
    `</div>`;
  document.body.appendChild(overlay);

  overlay.addEventListener("mousedown", (e) => {
    if (e.target === overlay) closePalette();
  });
  // The palette owns the keyboard while open: stop chords (F8, permission
  // shortcuts) from firing in the app underneath.
  overlay.addEventListener("keydown", (e: KeyboardEvent) => {
    e.stopPropagation();
    onKeydown(e);
  });

  const input = overlay.querySelector<HTMLInputElement>("#palette-input");
  input?.addEventListener("input", () => {
    query = input.value;
    selected = 0;
    editing = false;
    renderHits();
  });
  renderHits();
  input?.focus();
}

function renderHits(): void {
  const listEl = overlay?.querySelector("#palette-hits");
  const countEl = overlay?.querySelector("#palette-count");
  if (!listEl) return;
  const all = hits();
  if (selected >= all.length) selected = Math.max(0, all.length - 1);
  const total = deps?.index().length ?? 0;
  if (countEl) countEl.textContent = `${all.length} matches · ${total} settings indexed`;
  if (all.length === 0) {
    listEl.innerHTML = `<div class="palette-empty">${total === 0 ? "No settings loaded — is the daemon running?" : "No matches"}</div>`;
    return;
  }

  let html = "";
  let lastSection = "";
  all.forEach((hit, i) => {
    const v = hit.value;
    if (v.sectionLabel !== lastSection) {
      lastSection = v.sectionLabel;
      html += `<div class="palette-group">${escapeHtml(v.sectionLabel)}</div>`;
    }
    html +=
      `<div class="palette-hit${i === selected ? " sel" : ""}" data-i="${i}">` +
      `<span class="palette-path">${markPath(v.path, hit.positions)}</span>` +
      (v.restartRequired ? `<span class="palette-restart">restart</span>` : "") +
      `<span class="palette-cur${v.secretRef ? " vaultref" : ""}">${escapeHtml(displayValue(v))}</span>` +
      `</div>`;
    if (i === selected && editing) html += renderEditor(v);
  });
  listEl.innerHTML = html;

  listEl.querySelectorAll<HTMLElement>(".palette-hit").forEach((el) => {
    el.addEventListener("click", () => {
      selected = Number(el.dataset.i) || 0;
      editing = true;
      renderHits();
      focusEditor();
    });
  });
  wireEditor(listEl);
  listEl.querySelector(".palette-hit.sel")?.scrollIntoView({ block: "nearest" });
}

function renderEditor(v: SettingValue): string {
  if (v.readOnly) {
    return `<div class="palette-editor"><div class="palette-editor-hint">duplicate instance name — edit .humanconfig.yaml directly</div></div>`;
  }
  const isList = v.type === "stringlist" || v.type === "intlist";
  const raw = Array.isArray(v.value) ? (v.value as unknown[]).join(", ") : v.masked ? "" : String(v.value ?? "");
  const placeholder = v.masked
    ? "•••••• (unchanged — type to replace)"
    : isList
      ? "comma-separated"
      : "";
  return (
    `<div class="palette-editor">` +
    `<div class="palette-editor-label">${escapeHtml(v.label)}${v.description ? " — " + escapeHtml(v.description) : ""}</div>` +
    `<input id="palette-editor-input" type="text" autocomplete="off" spellcheck="false" value="${escapeHtml(raw)}" placeholder="${escapeHtml(placeholder)}" />` +
    `<div id="palette-editor-msg" class="palette-editor-hint">↵ save · esc back</div>` +
    `</div>`
  );
}

function wireEditor(listEl: Element): void {
  const input = listEl.querySelector<HTMLInputElement>("#palette-editor-input");
  input?.addEventListener("keydown", (e: KeyboardEvent) => {
    e.stopPropagation();
    if (e.key === "Escape") {
      e.preventDefault();
      editing = false;
      renderHits();
      overlay?.querySelector<HTMLInputElement>("#palette-input")?.focus();
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      void commitEditor(input);
    }
  });
}

function focusEditor(): void {
  overlay?.querySelector<HTMLInputElement>("#palette-editor-input")?.focus();
}

// paletteParse adapts the single-line editor to the field's type: lists are
// comma-separated here (the view's textarea uses one-per-line).
function paletteParse(v: SettingValue, raw: string): unknown {
  if (v.type === "stringlist" || v.type === "intlist") {
    const joined = raw
      .split(",")
      .map((s) => s.trim())
      .filter((s) => s !== "")
      .join("\n");
    return parseInput(v.type, joined);
  }
  if (v.type === "bool") return raw.trim() === "true";
  return parseInput(v.type, raw);
}

async function commitEditor(input: HTMLInputElement): Promise<void> {
  const hit = hits()[selected];
  if (!hit || !deps) return;
  const v = hit.value;
  if (v.masked && input.value === "") return; // write-only secret untouched
  const msg = overlay?.querySelector("#palette-editor-msg");
  let parsed: unknown;
  try {
    parsed = paletteParse(v, input.value);
  } catch (err) {
    if (msg) msg.textContent = String(err instanceof Error ? err.message : err);
    return;
  }
  try {
    await deps.save(v.path, parsed);
    editing = false;
    renderHits(); // fresh snapshot: the row now shows the saved value
    const row = overlay?.querySelector(".palette-hit.sel");
    row?.classList.add("saved");
    overlay?.querySelector<HTMLInputElement>("#palette-input")?.focus();
  } catch (err) {
    if (msg) msg.textContent = String(err instanceof Error ? err.message : err);
  }
}

function onKeydown(e: KeyboardEvent): void {
  if (editing) return; // the editor input has its own handler
  const all = hits();
  switch (e.key) {
    case "Escape":
      e.preventDefault();
      closePalette();
      break;
    case "ArrowDown":
      e.preventDefault();
      selected = Math.min(selected + 1, all.length - 1);
      renderHits();
      break;
    case "ArrowUp":
      e.preventDefault();
      selected = Math.max(selected - 1, 0);
      renderHits();
      break;
    case "Enter":
      if (all.length > 0) {
        e.preventDefault();
        editing = true;
        renderHits();
        focusEditor();
      }
      break;
    default:
      break;
  }
}
