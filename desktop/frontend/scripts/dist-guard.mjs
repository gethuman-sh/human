// The dist drift guard: does the committed desktop/frontend/dist/ still match
// what a build of the current src/ produces? (SC-3613)
//
// It answers that about MEANING, not bytes. `git diff --exit-code -- dist` used
// to stand in for the question, and a tsc emit differing by two trailing spaces
// was enough to fail it — a deploy-blocking red check no re-run could clear,
// because the CI job is read-only and cannot commit the rebuild it just made.
//
// Whole-line whitespace is normalized away; whitespace BETWEEN tokens on a line
// is deliberately kept significant. Collapsing it (git's --ignore-all-space)
// would also hide a changed string literal such as "Find Bugs" -> "FindBugs",
// and a guard that misses a genuinely stale bundle is the SC-1691 defect this
// exists to prevent.
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const GIT_MAX_BUFFER = 64 * 1024 * 1024;

export function normalizeEmit(text) {
  const lines = text.split(/\r?\n/).map((line) => line.trim());
  while (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  return lines.join("\n");
}

// entries: [{ name, committed: string|null, rebuilt: string|null }]
export function compareBundle(entries) {
  const drifted = [];
  const added = [];
  const removed = [];
  for (const { name, committed, rebuilt } of entries) {
    if (committed === null || committed === undefined) added.push(name);
    else if (rebuilt === null || rebuilt === undefined) removed.push(name);
    else if (normalizeEmit(committed) !== normalizeEmit(rebuilt)) drifted.push(name);
  }
  drifted.sort();
  added.sort();
  removed.sort();
  return { drifted, added, removed, ok: drifted.length + added.length + removed.length === 0 };
}

export function formatFailure(result) {
  const lines = ["the committed bundle differs from its rebuild"];
  if (result.drifted.length > 0) lines.push(`  changed:       ${result.drifted.join(", ")}`);
  if (result.added.length > 0) lines.push(`  not committed: ${result.added.join(", ")} (the build emits it, dist/ does not have it)`);
  if (result.removed.length > 0) lines.push(`  stale in dist/: ${result.removed.join(", ")} (committed, but the build no longer emits it)`);
  lines.push("");
  lines.push("Run `cd desktop/frontend && npm run build` and commit dist/.");
  lines.push("Whitespace-only differences are ignored, so this is a real difference in the emitted code.");
  return lines.join("\n");
}

function listFiles(dir, prefix = "") {
  const names = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) names.push(...listFiles(full, `${prefix}${entry}/`));
    else names.push(`${prefix}${entry}`);
  }
  return names;
}

function committedNames(root) {
  const out = execFileSync("git", ["ls-tree", "-r", "--name-only", "HEAD", "--", "dist"], {
    cwd: root,
    encoding: "utf8",
    maxBuffer: GIT_MAX_BUFFER,
  });
  return out
    .split("\n")
    .filter((line) => line.startsWith("dist/"))
    .map((line) => line.slice("dist/".length));
}

function committedText(root, name) {
  return execFileSync("git", ["show", `HEAD:./dist/${name}`], {
    cwd: root,
    encoding: "utf8",
    maxBuffer: GIT_MAX_BUFFER,
  });
}

export function collectEntries(root, read = { committedNames, committedText }) {
  const dist = resolve(root, "dist");
  const rebuilt = existsSync(dist) ? listFiles(dist) : [];
  const committed = read.committedNames(root);
  const names = [...new Set([...committed, ...rebuilt])].sort();
  return names.map((name) => ({
    name,
    committed: committed.includes(name) ? read.committedText(root, name) : null,
    rebuilt: rebuilt.includes(name) ? readFileSync(resolve(dist, name), "utf8") : null,
  }));
}

export function main(root) {
  const result = compareBundle(collectEntries(root));
  if (result.ok) {
    console.log("dist/ matches its rebuild");
    return 0;
  }
  console.error(formatFailure(result));
  return 1;
}

// Importable by the tests without running the guard; executable as a script.
const here = dirname(fileURLToPath(import.meta.url));
if (process.argv[1] && resolve(process.argv[1]) === resolve(here, "dist-guard.mjs")) {
  process.exit(main(process.argv[2] ? resolve(process.argv[2]) : resolve(here, "..")));
}
