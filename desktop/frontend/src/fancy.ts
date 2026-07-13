// fancy.ts — the optional "fancy" presentation layer: a second, demo-oriented
// theme (animated gradient, pastel rainbow columns — see the FANCY THEME
// section of style.css) plus celebration effects driven from here: fireworks,
// confetti, comet trail, dust motes, drop ripples and streak toasts.
//
// Design constraints, in priority order:
//   - Classic stays untouched: every exported hook no-ops unless the fancy
//     theme is active, so board.ts may call them unconditionally.
//   - One canvas, one rAF loop, zero idle work: the loop stops the moment the
//     particle pool drains; ambient motes keep it alive only while the fancy
//     theme is on AND the window is visible.
//   - prefers-reduced-motion keeps the fancy colors but disables all motion
//     (CSS handles its own animations via the same media query).
//
// Removing one effect = delete its EFFECTS flag, its call site and its CSS
// block; tsc's noUnusedLocals then points at any leftover helper.

const EFFECTS = {
  fireworks: true,
  confetti: true,
  trail: true,
  motes: true,
  ripple: true,
  tilt: true,
  spotlight: true,
  sheen: true,
  streak: true,
  sweep: true,
};

const THEME_KEY = "human.theme";
const MOTE_TARGET = 32;
const STREAK_WINDOW_MS = 10_000;

type Pt = { x: number; y: number };

interface Particle {
  kind: "fx" | "mote";
  x: number;
  y: number;
  vx: number;
  vy: number;
  age: number;
  ttl: number; // seconds; motes are ageless and live until the theme turns off
  size: number;
  hue: number;
  sat: number;
  lum: number;
  alpha: number;
  gravity: number;
  drag: number;
  shape: "dot" | "rect";
  rot: number;
  vr: number;
}

// --- Theme state --------------------------------------------------------

export function isFancy(): boolean {
  return document.documentElement.dataset.theme === "fancy";
}

export function toggleTheme(): void {
  const fancy = !isFancy();
  if (fancy) document.documentElement.dataset.theme = "fancy";
  else delete document.documentElement.dataset.theme;
  try {
    localStorage.setItem(THEME_KEY, fancy ? "fancy" : "classic");
  } catch {
    // Storage may be unavailable; the toggle still works for this session.
  }
  syncAmbient();
}

// isThemeToggleChord matches the style-toggle key (F8). Guarded against firing
// while the user types, so a future rebind to a printable key stays safe.
export function isThemeToggleChord(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null;
  if (t && t.closest("input, textarea, select, [contenteditable]")) return false;
  return e.key === "F8";
}

// initFancy wires the always-on listeners (cheap no-ops in classic) and starts
// ambient effects when the pre-paint script already applied the fancy theme.
export function initFancy(): void {
  document.addEventListener("visibilitychange", syncAmbient);
  document.addEventListener("pointermove", onPointerMove);
  syncAmbient();
}

// --- Board hooks (called unconditionally from board.ts) ------------------

// celebrateDrop fires the drop celebration at the release point: ripple always,
// confetti (escalated by the done-streak) for Done drops, fireworks otherwise.
// Runs before the optimistic re-render, so DOM lookups are deferred a tick.
export function celebrateDrop(pt: Pt, info: { key: string; fromStage: string; done: boolean }): void {
  if (!isFancy() || reducedMotion()) return;
  if (EFFECTS.ripple) ripple(pt);
  if (info.done && EFFECTS.confetti) {
    const streak = EFFECTS.streak ? bumpStreak() : 1;
    spawnConfetti(pt, Math.min(70 + (streak - 1) * 50, 240));
    if (streak >= 2) toast(`${streak}×!`);
    goldGlow(info.key);
  } else if (EFFECTS.fireworks) {
    spawnFirework(pt);
  }
  if (EFFECTS.sweep) scheduleClearSweep(info.fromStage);
}

// ghostTilt leans the drag ghost into the pointer's horizontal velocity, like
// a card picked up by one corner. The CSS transition springs it back upright.
export function ghostTilt(ghost: HTMLElement, dx: number): void {
  if (!EFFECTS.tilt || !isFancy() || reducedMotion()) return;
  const deg = Math.max(-10, Math.min(10, dx * 0.7));
  ghost.style.setProperty("--tilt", `${deg.toFixed(1)}deg`);
}

// trail sprinkles comet sparkles along the drag path. Throttled by distance so
// fast drags don't flood the pool.
let lastTrail: Pt | null = null;

export function trail(pt: Pt): void {
  if (!EFFECTS.trail || !isFancy() || reducedMotion()) return;
  if (lastTrail && Math.hypot(pt.x - lastTrail.x, pt.y - lastTrail.y) < 14) return;
  lastTrail = pt;
  spawn({
    kind: "fx",
    x: pt.x + rand(-6, 6),
    y: pt.y + rand(-6, 6),
    vx: rand(-12, 12),
    vy: rand(-4, 20),
    age: 0,
    ttl: rand(0.3, 0.6),
    size: rand(1.2, 2.6),
    hue: rand(0, 360),
    sat: 90,
    lum: 75,
    alpha: 0.9,
    gravity: 40,
    drag: 1,
    shape: "dot",
    rot: 0,
    vr: 0,
  });
}

// --- Streak tracking ------------------------------------------------------

let doneDrops: number[] = [];

function bumpStreak(): number {
  const now = performance.now();
  doneDrops = doneDrops.filter((t) => now - t < STREAK_WINDOW_MS);
  doneDrops.push(now);
  return doneDrops.length;
}

// --- DOM effects (ripple, toast, gold glow, column-clear sweep) -----------

function ripple(pt: Pt): void {
  const el = document.createElement("div");
  el.className = "fx-ripple";
  el.style.left = `${pt.x}px`;
  el.style.top = `${pt.y}px`;
  removeAfterAnimation(el, 700);
  document.body.appendChild(el);
}

function toast(text: string): void {
  const el = document.createElement("div");
  el.className = "fx-toast";
  el.textContent = text;
  removeAfterAnimation(el, 1600);
  document.body.appendChild(el);
}

// goldGlow crowns the card that just landed in Done. The card element is
// rebuilt by the optimistic render right after the drop, so look it up late.
function goldGlow(key: string): void {
  window.setTimeout(() => {
    const card = document.querySelector<HTMLElement>(`.card[data-key="${cssEscape(key)}"]`);
    if (!card) return;
    card.classList.add("fx-gold");
    window.setTimeout(() => card.classList.remove("fx-gold"), 1600);
  }, 60);
}

// scheduleClearSweep celebrates emptying a column: checked after the optimistic
// render has moved the dropped card out of its source column.
function scheduleClearSweep(fromStage: string): void {
  window.setTimeout(() => {
    if (!isFancy() || reducedMotion()) return;
    const left = document.querySelector(`.column[data-stage="${cssEscape(fromStage)}"] .card`);
    if (left) return;
    const el = document.createElement("div");
    el.className = "fx-sweep";
    removeAfterAnimation(el, 1400);
    document.body.appendChild(el);
  }, 80);
}

function removeAfterAnimation(el: HTMLElement, fallbackMs: number): void {
  el.addEventListener("animationend", () => el.remove());
  // Fallback: reduced-motion or a dropped animation must not leak the node.
  window.setTimeout(() => el.remove(), fallbackMs);
}

function cssEscape(v: string): string {
  return typeof CSS !== "undefined" && CSS.escape ? CSS.escape(v) : v.replace(/["\\]/g, "\\$&");
}

// --- Cursor spotlight + holographic sheen (shared pointermove) ------------

let spotlight: HTMLElement | null = null;

function onPointerMove(e: PointerEvent): void {
  if (!isFancy() || reducedMotion()) {
    if (spotlight) {
      spotlight.style.opacity = "0";
      spotlight.style.display = "none";
    }
    return;
  }
  if (EFFECTS.spotlight) {
    if (!spotlight) {
      spotlight = document.createElement("div");
      spotlight.className = "fx-spotlight";
      document.body.appendChild(spotlight);
    }
    spotlight.style.display = "";
    spotlight.style.opacity = "1";
    spotlight.style.transform = `translate(${e.clientX}px, ${e.clientY}px)`;
  }
  if (EFFECTS.sheen) {
    const card = (e.target as HTMLElement | null)?.closest?.(".card") as HTMLElement | null;
    if (card) {
      const r = card.getBoundingClientRect();
      card.style.setProperty("--mx", `${(((e.clientX - r.left) / r.width) * 100).toFixed(1)}%`);
      card.style.setProperty("--my", `${(((e.clientY - r.top) / r.height) * 100).toFixed(1)}%`);
    }
  }
}

// --- Particle engine ------------------------------------------------------

let canvas: HTMLCanvasElement | null = null;
let ctx: CanvasRenderingContext2D | null = null;
let pool: Particle[] = [];
let raf = 0;
let lastTs = 0;

function reducedMotion(): boolean {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function rand(min: number, max: number): number {
  return min + Math.random() * (max - min);
}

function ensureCanvas(): void {
  if (canvas) return;
  canvas = document.createElement("canvas");
  canvas.className = "fx-canvas";
  document.body.appendChild(canvas);
  ctx = canvas.getContext("2d");
  const fit = (): void => {
    if (!canvas || !ctx) return;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.floor(window.innerWidth * dpr);
    canvas.height = Math.floor(window.innerHeight * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  };
  fit();
  window.addEventListener("resize", fit);
}

function spawn(p: Particle): void {
  if (!isFancy() || reducedMotion()) return;
  ensureCanvas();
  pool.push(p);
  if (!raf) {
    lastTs = 0;
    raf = requestAnimationFrame(tick);
  }
}

function spawnFirework(pt: Pt): void {
  const baseHue = rand(0, 360);
  for (let i = 0; i < 44; i++) {
    const angle = rand(0, Math.PI * 2);
    const speed = rand(60, 340);
    spawn({
      kind: "fx",
      x: pt.x,
      y: pt.y,
      vx: Math.cos(angle) * speed,
      vy: Math.sin(angle) * speed - 40,
      age: 0,
      ttl: rand(0.5, 1.1),
      size: rand(1.5, 3),
      hue: (baseHue + rand(-30, 30) + 360) % 360,
      sat: 95,
      lum: 68,
      alpha: 1,
      gravity: 260,
      drag: 1.6,
      shape: "dot",
      rot: 0,
      vr: 0,
    });
  }
}

function spawnConfetti(pt: Pt, count: number): void {
  for (let i = 0; i < count; i++) {
    const angle = rand(-Math.PI * 0.85, -Math.PI * 0.15); // upward fan
    const speed = rand(140, 480);
    spawn({
      kind: "fx",
      x: pt.x,
      y: pt.y,
      vx: Math.cos(angle) * speed,
      vy: Math.sin(angle) * speed,
      age: 0,
      ttl: rand(0.9, 1.8),
      size: rand(3, 6),
      hue: rand(0, 360),
      sat: 85,
      lum: 70,
      alpha: 1,
      gravity: 420,
      drag: 1.2,
      shape: "rect",
      rot: rand(0, Math.PI),
      vr: rand(-8, 8),
    });
  }
}

function makeMote(atBottom: boolean): Particle {
  return {
    kind: "mote",
    x: rand(0, window.innerWidth),
    y: atBottom ? window.innerHeight + 4 : rand(0, window.innerHeight),
    vx: rand(-4, 4),
    vy: rand(-14, -5),
    age: 0,
    ttl: Infinity,
    size: rand(0.8, 2.2),
    hue: rand(0, 360),
    sat: 60,
    lum: 80,
    alpha: rand(0.1, 0.32),
    gravity: 0,
    drag: 0,
    shape: "dot",
    rot: 0,
    vr: 0,
  };
}

// syncAmbient reconciles the particle layer with the current theme and window
// visibility: motes drift only while fancy is on-screen, leaving fancy kills
// every particle at once, and the loop pauses (with a wiped frame) while the
// window is hidden so a stale frame is never left on screen.
function syncAmbient(): void {
  const visible = document.visibilityState === "visible";
  const ambientOn = isFancy() && !reducedMotion() && visible;
  if (spotlight && !ambientOn) {
    // display:none instead of a fade: classic must be pristine the instant
    // the theme switches, not 0.3s later. onPointerMove re-shows it in fancy.
    spotlight.style.opacity = "0";
    spotlight.style.display = "none";
  }
  if (!isFancy()) {
    // Leaving fancy: classic must be pristine instantly, so kill mid-flight
    // celebrations (particles AND overlay nodes) instead of letting them fade.
    pool = [];
    document.querySelectorAll(".fx-ripple, .fx-toast, .fx-sweep").forEach((n) => n.remove());
  } else if (ambientOn && EFFECTS.motes && !pool.some((p) => p.kind === "mote")) {
    ensureCanvas();
    for (let i = 0; i < MOTE_TARGET; i++) pool.push(makeMote(false));
  } else if (!ambientOn) {
    pool = pool.filter((p) => p.kind !== "mote");
  }
  if (!visible || pool.length === 0) {
    if (raf) {
      cancelAnimationFrame(raf);
      raf = 0;
    }
    if (ctx) ctx.clearRect(0, 0, window.innerWidth, window.innerHeight);
  } else if (!raf) {
    lastTs = 0;
    raf = requestAnimationFrame(tick);
  }
}

function tick(ts: number): void {
  if (!ctx || !canvas) {
    raf = 0;
    return;
  }
  const dt = lastTs ? Math.min((ts - lastTs) / 1000, 0.05) : 0.016;
  lastTs = ts;

  const w = window.innerWidth;
  const h = window.innerHeight;
  ctx.clearRect(0, 0, w, h);

  const alive: Particle[] = [];
  for (const p of pool) {
    p.age += dt;
    if (p.age >= p.ttl) continue;
    p.vx -= p.vx * p.drag * dt;
    p.vy += p.gravity * dt - p.vy * p.drag * dt;
    p.x += p.vx * dt;
    p.y += p.vy * dt;
    p.rot += p.vr * dt;

    if (p.kind === "mote") {
      // Motes recycle: drift off the top, re-enter from the bottom.
      if (p.y < -6) Object.assign(p, makeMote(true));
    } else if (p.y > h + 20 || p.x < -20 || p.x > w + 20) {
      continue;
    }

    const fade = p.ttl === Infinity ? 1 : 1 - p.age / p.ttl;
    ctx.globalAlpha = Math.max(0, p.alpha * fade);
    ctx.fillStyle = `hsl(${p.hue} ${p.sat}% ${p.lum}%)`;
    if (p.shape === "rect") {
      ctx.save();
      ctx.translate(p.x, p.y);
      ctx.rotate(p.rot);
      ctx.fillRect(-p.size / 2, -p.size, p.size, p.size * 2);
      ctx.restore();
    } else {
      ctx.beginPath();
      ctx.arc(p.x, p.y, p.size, 0, Math.PI * 2);
      ctx.fill();
    }
    alive.push(p);
  }
  ctx.globalAlpha = 1;
  pool = alive;

  raf = pool.length ? requestAnimationFrame(tick) : 0;
  if (!raf) ctx.clearRect(0, 0, w, h);
}
