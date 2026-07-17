// Pure builder for the board detail panel's comment-sourced sections, kept
// free of DOM and Wails bindings so it can be unit-tested directly (mirrors
// board-queue.ts). The daemon renders and sanitizes each field's HTML; this
// module only wraps present, non-blank fields in titled sections and drops
// absent ones so the panel never shows an empty heading.
// buildDetailSections returns the HTML for the comment-sourced detail sections,
// in fixed order: failure reason, review findings, fix summary. Each present,
// non-blank field becomes a titled <section>; absent or blank fields emit
// nothing. Section titles are static literals; only the daemon-sanitized *HTML
// values are injected, and the caller injects the result verbatim.
export function buildDetailSections(d) {
    const sections = [];
    const add = (title, html, cls) => {
        if (html && html.trim()) {
            sections.push(`<section class="detail-section ${cls}"><h3 class="detail-section-title">${title}</h3><div class="detail-section-body rendered">${html}</div></section>`);
        }
    };
    add("Why it failed", d.failureReasonHTML, "detail-failure");
    add("What the review found", d.reviewFindingsHTML, "detail-review");
    add("Fix summary", d.fixSummaryHTML, "detail-fixsummary");
    return sections.join("");
}
