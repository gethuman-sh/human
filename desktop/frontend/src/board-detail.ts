// Pure builder for the board detail panel's comment-sourced sections, kept
// free of DOM and Wails bindings so it can be unit-tested directly (mirrors
// board-queue.ts). The daemon renders and sanitizes each field's HTML; this
// module only wraps present, non-blank fields in titled sections and drops
// absent ones so the panel never shows an empty heading.

export interface DetailSections {
  reviewFindingsHTML?: string;
  failureReasonHTML?: string;
  fixSummaryHTML?: string;
}

export interface DetailOption {
  id: string;
  label: string;
}

// escapeText is a minimal HTML escaper for the option labels/context, which
// come from ticket comments (agent-written, but still untrusted text).
function escapeText(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

// buildOptionsSection renders a card's open decision block: the one-line
// context and one button per option. The buttons carry data-option-id; the
// caller wires the click to ChooseOption. Empty options emit nothing.
export function buildOptionsSection(context: string | undefined, options: DetailOption[] | undefined): string {
  if (!options || options.length === 0) return "";
  const ctx = context && context.trim() ? `<div class="detail-options-context">${escapeText(context)}</div>` : "";
  const buttons = options
    .map(
      (o) =>
        `<button type="button" class="detail-option-btn" data-option-id="${escapeText(o.id)}">` +
        `<span class="detail-option-id">${escapeText(o.id)}</span>${escapeText(o.label)}</button>`,
    )
    .join("");
  return (
    `<section class="detail-section detail-options"><h3 class="detail-section-title">Decision needed</h3>` +
    `${ctx}<div class="detail-options-list">${buttons}</div></section>`
  );
}

// buildDetailSections returns the HTML for the comment-sourced detail sections,
// in fixed order: failure reason, review findings, fix summary. Each present,
// non-blank field becomes a titled <section>; absent or blank fields emit
// nothing. Section titles are static literals; only the daemon-sanitized *HTML
// values are injected, and the caller injects the result verbatim.
export function buildDetailSections(d: DetailSections): string {
  const sections: string[] = [];
  const add = (title: string, html: string | undefined, cls: string): void => {
    if (html && html.trim()) {
      sections.push(
        `<section class="detail-section ${cls}"><h3 class="detail-section-title">${title}</h3><div class="detail-section-body rendered">${html}</div></section>`,
      );
    }
  };
  add("Why it failed", d.failureReasonHTML, "detail-failure");
  add("What the review found", d.reviewFindingsHTML, "detail-review");
  add("Fix summary", d.fixSummaryHTML, "detail-fixsummary");
  return sections.join("");
}
