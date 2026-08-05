// Board appearance applied from config (SC-3409). The dimming strength travels
// as an integer percent because the settings schema has no float type, and it
// lands on ONE custom property so every rule that derives from it — the hover
// return, the degraded/not-mine overlap's calc() — follows without being
// touched. The stylesheet's :root declaration stays the shipped fallback, so
// this only ever overrides a value that already works: an inline declaration
// wins by cascade order, above normal author rules.

// Kept in lockstep with internal/appearance's MinDimPercent/MaxDimPercent. The
// backend already rejects anything outside the range; re-checking here means a
// payload from an older or newer daemon can never blank the board.
const MIN_DIM_PERCENT = 5;
const MAX_DIM_PERCENT = 100;

export const NOT_MINE_OPACITY_VAR = "--not-mine-opacity";

// Minimal shape of what carries the property, so this is testable without a
// DOM. The signatures mirror lib.dom's CSSStyleDeclaration exactly (in
// particular removeProperty returning string) so document.documentElement.style
// is assignable under strict.
export interface StyledRoot {
  style: { setProperty(name: string, value: string): void; removeProperty(name: string): string };
}

// notMineOpacity converts a declared percent into the CSS opacity to set, or
// null for "no opinion" — undefined (nothing declared), non-integral, or out of
// range. Null is not the same as 0.35: it means DO NOT WRITE, so the value in
// the stylesheet is what applies.
export function notMineOpacity(dimPercent: number | undefined): string | null {
  if (typeof dimPercent !== "number" || !Number.isInteger(dimPercent)) return null;
  if (dimPercent < MIN_DIM_PERCENT || dimPercent > MAX_DIM_PERCENT) return null;
  return String(dimPercent / 100);
}

// applyNotMineOpacity writes the resolved value onto the document root, or
// REMOVES the property when there is nothing to say. Removing matters as much
// as setting: an inline custom property outlives the payload that set it, so
// without this, clearing dim_percent from the config would leave the old
// dimming stuck until the window is closed.
export function applyNotMineOpacity(root: StyledRoot, dimPercent: number | undefined): void {
  const value = notMineOpacity(dimPercent);
  if (value === null) {
    root.style.removeProperty(NOT_MINE_OPACITY_VAR);
    return;
  }
  root.style.setProperty(NOT_MINE_OPACITY_VAR, value);
}
