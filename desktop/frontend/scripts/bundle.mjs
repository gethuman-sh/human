// Minimal "bundler" for the board frontend: tsc emits ES modules to build/, and
// this script assembles the dist/ that Wails embeds (//go:embed all:frontend/dist).
// It copies every compiled module plus the static index.html and style.css.
// Kept dependency-free on purpose so `wails build` needs only Node + tsc.
import { copyFileSync, mkdirSync, existsSync, readdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "..");
const dist = resolve(root, "dist");
mkdirSync(dist, { recursive: true });

// Copy every emitted module: board.ts imports sibling modules (e.g. fancy.js),
// and a missing one would leave the embedded app with a broken import.
const build = resolve(root, "build");
if (existsSync(build)) {
  for (const f of readdirSync(build).filter((f) => f.endsWith(".js"))) {
    copyFileSync(resolve(build, f), resolve(dist, f));
  }
}

for (const f of ["index.html", "style.css"]) {
  const src = resolve(root, "static", f);
  if (existsSync(src)) copyFileSync(src, resolve(dist, f));
}

console.log("frontend bundled into dist/");
