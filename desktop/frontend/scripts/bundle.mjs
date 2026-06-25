// Minimal "bundler" for the board frontend: tsc emits ES modules to build/, and
// this script assembles the dist/ that Wails embeds (//go:embed all:frontend/dist).
// It copies the compiled board.js plus the static index.html and style.css.
// Kept dependency-free on purpose so `wails build` needs only Node + tsc.
import { copyFileSync, mkdirSync, existsSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "..");
const dist = resolve(root, "dist");
mkdirSync(dist, { recursive: true });

const compiled = resolve(root, "build", "board.js");
if (existsSync(compiled)) {
  copyFileSync(compiled, resolve(dist, "board.js"));
}

for (const f of ["index.html", "style.css"]) {
  const src = resolve(root, "static", f);
  if (existsSync(src)) copyFileSync(src, resolve(dist, f));
}

console.log("frontend bundled into dist/");
