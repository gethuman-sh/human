"use strict";

// The web console is read-only: it polls the JSON snapshot endpoint and repaints
// the three panes. No state is kept beyond the latest snapshot, so a missed poll
// self-heals on the next tick. Mirrors the old TUI's 2s refresh cadence.
const REFRESH_MS = 2000;

const el = (id) => document.getElementById(id);

// A faint marker for an absent value, matching the TUI's em-dash convention.
const DASH = "—";

function setStatus(text, cls) {
  const s = el("status");
  s.textContent = text;
  s.className = cls || "dim";
}

function renderCapacity(cap) {
  el("capacity").innerHTML =
    `Capacity: <strong>${cap.daemons}</strong> daemons · ` +
    `<span class="busy">${cap.busy} busy</span> · ` +
    `<span class="blocked">${cap.blocked} blocked</span> · ` +
    `${cap.idle} idle`;
}

function renderBoard(board) {
  const body = el("board-body");
  const empty = el("board-empty");
  body.innerHTML = "";
  if (!board || board.length === 0) {
    empty.style.display = "block";
    return;
  }
  empty.style.display = "none";
  for (const w of board) {
    const tr = document.createElement("tr");
    tr.appendChild(cell(w.daemon));
    tr.appendChild(cell(w.ticket));
    tr.appendChild(cell(w.repo || DASH));
    tr.appendChild(cell(w.branch || DASH));
    tr.appendChild(cell(w.state || DASH, w.state ? `state-${w.state}` : null));
    body.appendChild(tr);
  }
}

function renderBurn(burnTicket, burnRepo) {
  const empty = el("burn-empty");
  const hasAny = (burnTicket && burnTicket.length) || (burnRepo && burnRepo.length);
  empty.style.display = hasAny ? "none" : "block";
  fillBurn("burn-ticket-body", burnTicket);
  fillBurn("burn-repo-body", burnRepo);
}

function fillBurn(bodyId, rows) {
  const body = el(bodyId);
  body.innerHTML = "";
  if (!rows || rows.length === 0) {
    const tr = document.createElement("tr");
    tr.appendChild(cell(DASH, "dim"));
    tr.appendChild(cell(""));
    body.appendChild(tr);
    return;
  }
  for (const r of rows) {
    const tr = document.createElement("tr");
    tr.appendChild(cell(r.key || DASH));
    tr.appendChild(cell(r.display));
    body.appendChild(tr);
  }
}

function cell(text, cls) {
  const td = document.createElement("td");
  td.textContent = text;
  if (cls) td.className = cls;
  return td;
}

async function poll() {
  try {
    const res = await fetch("api/snapshot", { cache: "no-store" });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const snap = await res.json();
    renderCapacity(snap.capacity);
    renderBoard(snap.board);
    renderBurn(snap.burn_by_ticket, snap.burn_by_repo);
    setStatus("live", "live");
  } catch (err) {
    setStatus(`disconnected (${err.message})`, "stale");
  }
}

poll();
setInterval(poll, REFRESH_MS);
