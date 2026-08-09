// SC-3613 regression: the dist drift guard must compare meaning, not bytes.
//
// `.github/workflows/desktop.yml` used to answer "is the committed bundle
// current?" with `git diff --exit-code -- dist`. On 2026-08-04 that turned two
// trailing spaces in a tsc emit into a red frontend-test that blocked every
// deploy and that no re-run could clear. The fixtures below are the exact two
// blobs from that failure, so the guard's two verdicts are pinned by the real
// bytes rather than by argument.
//
// Builtins only, no build/ import: this must run where the npm toolchain is
// absent (the same reason board-error-guard.test.mjs is source-level).
import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { normalizeEmit, compareBundle, formatFailure } from "./dist-guard.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const fixtures = resolve(here, "testdata", "sc3613");
const committed = readFileSync(resolve(fixtures, "board.committed.js"), "utf8");
const rebuilt = readFileSync(resolve(fixtures, "board.rebuilt.js"), "utf8");

// A git blob hash over the fixture bytes: cheap proof these are still the
// historical files and not something a formatter walked through.
function blobHash(path) {
  const bytes = readFileSync(path);
  return createHash("sha1").update(`blob ${bytes.length}\0`).update(bytes).digest("hex");
}

test("the fixtures are the exact blobs from the SC-3613 failure", () => {
  assert.equal(blobHash(resolve(fixtures, "board.committed.js")), "df0b06e37a62b469dc365c841229bac58c992d2b");
  assert.equal(blobHash(resolve(fixtures, "board.rebuilt.js")), "1ff98ceded43827d6720e7c4baf8bf40bd7d00ef");
  // The byte comparison the old guard made — this is the failure being fixed.
  assert.notEqual(committed, rebuilt, "fixtures must differ in bytes, or the test proves nothing");
});

test("whitespace-only rebuild noise is not drift", () => {
  const result = compareBundle([{ name: "board.js", committed, rebuilt }]);
  assert.deepEqual(result.drifted, []);
  assert.ok(result.ok, "the two-trailing-space pair must pass the guard");
});

test("a real call-site change is drift", () => {
  const guarded =
    "await runGuardedAction(() => go().FindRelatedWork(key, title), (err) => showError(errMessage(err)), reconcile);";
  const bare = "await go().FindRelatedWork(key, title).catch((err) => showError(errMessage(err)));";
  assert.ok(rebuilt.includes(guarded), "fixture must still contain the call site the mutation targets");
  const stale = rebuilt.replace(guarded, bare);
  const result = compareBundle([{ name: "board.js", committed: stale, rebuilt }]);
  assert.deepEqual(result.drifted, ["board.js"]);
  assert.equal(result.ok, false, "a bundle whose behaviour lags src/ must still fail");
});

test("a module the rebuild emits but dist/ does not carry is drift", () => {
  const result = compareBundle([{ name: "fancy.js", committed: null, rebuilt: "export const x = 1;\n" }]);
  assert.deepEqual(result.added, ["fancy.js"]);
  assert.equal(result.ok, false);
});

test("a module dist/ carries but the rebuild no longer emits is drift", () => {
  const result = compareBundle([{ name: "gone.js", committed: "export const x = 1;\n", rebuilt: null }]);
  assert.deepEqual(result.removed, ["gone.js"]);
  assert.equal(result.ok, false);
});

test("normalizeEmit folds only whole-line whitespace", () => {
  assert.equal(normalizeEmit("a();  \nb();\n"), normalizeEmit("a();\nb();"));
  assert.equal(normalizeEmit("if (x) {\r\n  y();\r\n}\r\n"), normalizeEmit("if (x) {\n    y();\n}\n"));
  assert.equal(normalizeEmit("a();\n\n\n"), normalizeEmit("a();\n"));
  // Kept significant on purpose: collapsing these would blind the guard to a
  // changed string literal, which is exactly the staleness it exists to catch.
  assert.notEqual(normalizeEmit('t("Find Bugs");'), normalizeEmit('t("FindBugs");'));
});

test("the failure message names its own remedy", () => {
  const message = formatFailure(compareBundle([{ name: "board.js", committed: "a();", rebuilt: "b();" }]));
  assert.match(message, /board\.js/);
  assert.match(message, /npm run build/);
  assert.match(message, /commit/i);
});

// The workflow must not go back to byte equality: that regression is invisible
// to every test above, because it lives in YAML rather than in this module.
test("the CI guard step runs this module, not a byte diff", () => {
  const workflow = readFileSync(resolve(here, "..", "..", "..", ".github", "workflows", "desktop.yml"), "utf8");
  assert.match(workflow, /node scripts\/dist-guard\.mjs/);
  assert.doesNotMatch(workflow, /git diff\s+--exit-code\s+--\s+dist/);
});

// End to end over a real repository: the exit status is the guard's verdict.
test("the script exits 0 on whitespace noise and 1 on a real change", () => {
  const repo = mkdtempSync(resolve(tmpdir(), "sc3613-"));
  try {
    const root = resolve(repo, "desktop", "frontend");
    mkdirSync(resolve(root, "dist"), { recursive: true });
    const git = (...args) =>
      execFileSync("git", ["-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", ...args], {
        cwd: repo,
        encoding: "utf8",
      });
    writeFileSync(resolve(root, "dist", "board.js"), "const a = 1;\n");
    git("init", "-q", "-b", "main", ".");
    git("add", "-A");
    git("commit", "-qm", "fixture");

    const guard = resolve(here, "dist-guard.mjs");
    const run = () => {
      try {
        execFileSync(process.execPath, [guard, root], { cwd: repo, encoding: "utf8", stdio: "pipe" });
        return 0;
      } catch (err) {
        return err.status;
      }
    };

    writeFileSync(resolve(root, "dist", "board.js"), "const a = 1;  \n");
    assert.equal(run(), 0, "a trailing space must not fail the guard");

    writeFileSync(resolve(root, "dist", "board.js"), "const a = 2;\n");
    assert.equal(run(), 1, "a changed value must fail the guard");
  } finally {
    rmSync(repo, { recursive: true, force: true });
  }
});
