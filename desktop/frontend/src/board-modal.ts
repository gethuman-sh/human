// Pure helpers for the board's shared dialog shell, kept free of DOM and Wails
// bindings so they can be unit-tested directly (mirrors board-descedit.ts).

// A confirm button's valence. The shared .modal-confirm treatment is the
// constructive one (SC-5033): a destructive confirm says so, rather than every
// constructive dialog opting out of a red default one selector at a time.
export type ConfirmValence = "destructive" | "constructive";

// confirmButtonClass is the class list for a confirm button of the given
// valence. Constructive adds nothing: it IS the shared default, which is what
// makes a newly added constructive confirm correct with no new override.
export function confirmButtonClass(valence: ConfirmValence = "constructive"): string {
  return valence === "destructive" ? "modal-confirm modal-destructive" : "modal-confirm";
}

// isTopmostOverlay answers whether an overlay is the one on top. Escape is
// bound on document (the wizard's first screens focus nothing, so a
// modal-scoped listener would never fire there), and dialogs stack — the
// description editor's discard confirmation sits over the editor itself — so
// without this one Escape would dismiss both.
export function isTopmostOverlay(overlays: readonly unknown[], overlay: unknown): boolean {
  return overlays.length > 0 && overlays[overlays.length - 1] === overlay;
}
