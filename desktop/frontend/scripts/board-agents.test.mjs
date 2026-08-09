// SC-3603 regression: the "Running agents" pane went blank on a payload it
// did not expect. Instances() was read raw and drawn outside any error
// boundary, so a shape the pane did not expect threw during draw, the throw
// was discarded by `void pollAgents()`, and #agents — which ships empty —
// stayed blank forever at the 2s poll. This is SC-3508's defect on a second
// pane; SC-3508 (commit af055022) fixed it for statsview.ts and deliberately
// scoped board.ts out.
//
// Both src/board.ts and the committed dist/board.js are scanned and executed.
// dist/ is not a throwaway build artifact here — it is checked in and
// embedded via `//go:embed all:frontend/dist`, so a dist/ that lagged a fixed
// src/ would still ship the blank pane to users.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const srcSrc = readFileSync(resolve(here, "..", "src", "board.ts"), "utf8");
const distSrc = readFileSync(resolve(here, "..", "dist", "board.js"), "utf8");
const START = "// --- Running agents view";
const END = "// --- Features view";

// Blank comments to spaces (preserving offsets and newlines) so prose can't
// register as code. Copied verbatim from board-error-guard.test.mjs.
function blankComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, " "))
    .replace(/(^|[^:])\/\/[^\n]*/g, (m, lead) => lead + " ".repeat(m.length - lead.length));
}

// Index of the `}` closing the `{` at `open`. Copied verbatim from
// board-error-guard.test.mjs.
function matchBrace(src, open) {
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}" && --depth === 0) return i;
  }
  return -1;
}

const lineOf = (src, idx) => src.slice(0, idx).split("\n").length;

function agentsRegion(src) {
  const start = src.indexOf(START);
  const end = src.indexOf(END, start);
  assert.ok(start >= 0 && end > start, "the agents-view region markers must still delimit the pane");
  return src.slice(start, end);
}

// The pane lives in board.ts, the frontend's entry script, which cannot be
// imported from node (its top level touches the DOM). Slicing the region out
// of the SHIPPED bundle and evaluating it is how the pane is driven at all —
// and it is closer to what users run than an import would be.
function makePane(instances) {
  const el = {
    html: "",
    text: "",
    throwOn: () => false,
    get innerHTML() {
      return this.html;
    },
    set innerHTML(markup) {
      if (this.throwOn(markup)) throw new Error("host rejected the markup");
      this.html = markup;
    },
    get textContent() {
      return this.text;
    },
    set textContent(t) {
      this.text = t;
    },
  };
  const doc = { getElementById: (id) => (id === "agents" ? el : null) };
  const win = { setInterval: () => 1, clearInterval: () => {} };
  // Copies of src/board.ts:2831-2841.
  const escapeHtml = (s) => String(s ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
  const errMessage = (e) => (e instanceof Error ? e.message : String(e));
  const factory = new Function(
    "go",
    "escapeHtml",
    "errMessage",
    "window",
    "document",
    agentsRegion(distSrc) + "\nreturn { pollAgents, renderAgents };",
  );
  return { el, api: factory(() => ({ Instances: instances }), escapeHtml, errMessage, win, doc) };
}

function fullAgentRow() {
  return {
    label: "a",
    source: "cli",
    status: "working",
    hasActivity: true,
    slug: "sc-3603",
    pid: 123,
    containerID: "abc123",
    cwd: "/tmp/work",
    memory: "512M",
    currentTool: "Bash",
    blockedTool: "",
    errorType: "",
    startedAtUnix: Math.floor(Date.now() / 1000) - 30,
    daemonConnected: true,
    proxyConfigured: true,
    models: [{ name: "sonnet", inputTokens: 100, outputTokens: 50 }],
    tasksPending: 1,
    tasksInProgress: 1,
    tasksDone: 1,
    subagents: [
      {
        description: "do the thing",
        type: "explore",
        done: false,
        startedAtUnix: Math.floor(Date.now() / 1000) - 5,
        durationMs: 0,
      },
    ],
  };
}

// --- criterion (a): a payload missing a field the agents pane expects
// renders as an empty pane, not a blank one. ---------------------------

for (const [name, payload] of [
  ["a payload with no agents field renders the empty pane", {}],
  ["a null agents list renders the empty pane", { agents: null }],
  ["a null payload renders the empty pane", null],
  ["an undefined payload renders the empty pane", undefined],
  ["a retyped agents field degrades like a missing one", { agents: {} }],
  ["a list with a null row does not throw", { agents: [null] }],
  ["a minimal row with only a label still renders", { agents: [{ label: "a" }] }],
  [
    "a row with null models and subagents still renders",
    { agents: [{ label: "a", models: null, subagents: null }] },
  ],
]) {
  test(`SC-3603: ${name}`, async () => {
    const { el, api } = makePane(async () => payload);
    await assert.doesNotReject(api.pollAgents());
    assert.notEqual(el.innerHTML, "");
    assert.match(el.innerHTML, /agents-header/);
    assert.ok(/agents-empty/.test(el.innerHTML) || /agent-row/.test(el.innerHTML));
  });
}

test("SC-3603: a null row does not cost its neighbours", async () => {
  const { el, api } = makePane(async () => ({ agents: [null, { ...fullAgentRow(), label: "b" }] }));
  await assert.doesNotReject(api.pollAgents());
  const rowCount = (el.innerHTML.match(/agent-row/g) || []).length;
  assert.equal(rowCount, 2, "both the null row and its neighbour must render a row");
  assert.match(el.innerHTML, />b</);
});

// --- criterion (c): both directions hold for any future field added to or
// removed from the Instances() payload. --------------------------------

test("SC-3603: dropping any single top-level field of a full payload still renders", async () => {
  const full = { agents: [fullAgentRow()] };
  for (const key of Object.keys(full)) {
    const variant = { ...full };
    delete variant[key];
    const { el, api } = makePane(async () => variant);
    await assert.doesNotReject(api.pollAgents(), `dropping top-level "${key}" must not reject`);
    assert.notEqual(el.innerHTML, "", `dropping top-level "${key}" must not blank the pane`);
  }
});

test("SC-3603: dropping any single row field of a full payload still renders", async () => {
  const row = fullAgentRow();
  for (const key of Object.keys(row)) {
    const variant = { ...row };
    delete variant[key];
    const { el, api } = makePane(async () => ({ agents: [variant] }));
    await assert.doesNotReject(api.pollAgents(), `dropping row field "${key}" must not reject`);
    assert.notEqual(el.innerHTML, "", `dropping row field "${key}" must not blank the pane`);
  }
});

test("SC-3603: a field the app has never heard of is ignored", async () => {
  const full = { agents: [fullAgentRow()] };
  const { el: elFull, api: apiFull } = makePane(async () => full);
  await apiFull.pollAgents();

  const withExtra = {
    agents: [{ ...fullAgentRow(), futureField: { nested: 1 } }],
    newTopLevel: 7,
  };
  const { el: elExtra, api: apiExtra } = makePane(async () => withExtra);
  await assert.doesNotReject(apiExtra.pollAgents());
  assert.equal(elExtra.innerHTML, elFull.innerHTML, "an unknown field must not change the render");
});

// --- criterion (b): a fault while drawing the agents pane surfaces the same
// error banner a failed fetch already produces. -------------------------

test("SC-3603: a fault while drawing surfaces the fetch-error banner", async () => {
  const { el, api } = makePane(async () => ({ agents: [fullAgentRow()] }));
  el.throwOn = (m) => m.includes("agent-row");
  await assert.doesNotReject(api.pollAgents());
  assert.match(el.innerHTML, /class="banner"/);
  assert.match(el.innerHTML, /out of date/);
});

test("SC-3603: a host that rejects every write still gets plain text", async () => {
  const { el, api } = makePane(async () => ({ agents: [fullAgentRow()] }));
  el.throwOn = () => true;
  await assert.doesNotReject(api.pollAgents());
  assert.match(el.textContent, /out of date/);
});

test("SC-3603: three faulting ticks never wedge the poll", async () => {
  const { el, api } = makePane(async () => ({ agents: [fullAgentRow()] }));
  el.throwOn = (m) => m.includes("agent-row");
  for (let i = 0; i < 3; i++) {
    await assert.doesNotReject(api.pollAgents());
    assert.match(el.innerHTML, /out of date/, `tick ${i} must leave the banner painted`);
  }
});

// --- Part 2: source-level scanner over both src/board.ts and dist/board.js,
// asserting agentsData is only ever assigned through instancesFromPayload(),
// and that renderAgents() delegates to a guarded paint. -----------------

export function agentsGuardViolations(rawSrc, label) {
  const src = blankComments(rawSrc);
  const violations = [];
  // Every assignment to agentsData — the declaration included — must come
  // through the door, so the variable can only ever hold a normalized value.
  const assign = /\bagentsData\s*(?::\s*[\w<>[\]| ]+\s*)?=(?!=)\s*/g;
  let m;
  while ((m = assign.exec(src))) {
    const rhs = src.slice(m.index + m[0].length);
    if (!rhs.startsWith("instancesFromPayload(")) {
      violations.push(`${label}:${lineOf(rawSrc, m.index)} — agentsData assigned without instancesFromPayload()`);
    }
  }
  // The draw must be reached only through a guard.
  const fn = src.search(/function\s+renderAgents\s*\(\s*\)/);
  if (fn < 0) violations.push(`${label} — renderAgents() not found`);
  else {
    const open = src.indexOf("{", fn);
    const body = src.slice(open + 1, matchBrace(src, open));
    if (!/try\s*\{[\s\S]*paintAgents\s*\([\s\S]*\}\s*[\r\n]*\s*catch[\s\S]*paintAgentsFault\s*\(/.test(body)) {
      violations.push(
        `${label}:${lineOf(rawSrc, fn)} — renderAgents() must delegate to paintAgents() inside a try whose catch calls paintAgentsFault()`,
      );
    }
  }
  return violations;
}

for (const [label, source] of [
  ["src/board.ts", srcSrc],
  ["dist/board.js", distSrc],
]) {
  test(`SC-3603: the agents payload is normalized before anything renders it (${label})`, () => {
    const violations = agentsGuardViolations(source, label);
    assert.deepEqual(violations, [], violations.join("\n  "));
  });
}

// Guard the guard: a scanner that silently matches nothing would pass
// forever.
test("SC-3603: the scanner detects a raw assignment and an unguarded render", () => {
  const broken = `
    let agentsData = instancesFromPayload({});
    async function pollAgents() {
      agentsData = await go().Instances();
      renderAgents();
    }
    function renderAgents() {
      const host = document.getElementById("agents");
      if (!host) return;
      host.innerHTML = "whatever";
    }`;
  assert.equal(agentsGuardViolations(broken, "broken").length, 2);
});

test("SC-3603: the scanner accepts the fixed shape", () => {
  const fixed = `
    let agentsData = instancesFromPayload({});
    async function pollAgents() {
      agentsData = instancesFromPayload(await go().Instances());
      renderAgents();
    }
    function renderAgents() {
      const host = document.getElementById("agents");
      if (!host) return;
      try {
        paintAgents(host);
      } catch (err) {
        paintAgentsFault(host, err);
      }
    }`;
  assert.deepEqual(agentsGuardViolations(fixed, "fixed"), []);
});
