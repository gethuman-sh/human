---
name: human-mockups
description: Create annotated static HTML mockups exploring UI options for a feature, matched to the project's real look
argument-hint: <feature to explore> [number of options, default 5]
---

# UI Option Mockups

Produce N static HTML mockups (default 5), each showing a DIFFERENT interaction pattern for the requested feature, so the options can be compared side by side and one can be picked for implementation. A picked mockup can be iterated further via the variation invocation below and ultimately marked as the ticket's winner in the app, whereupon it is handed to planning/execution agents as the design direction. No functionality, no JavaScript — these are pictures made of HTML.

All files for one invocation go into their own subdirectory `mockups/<feature-slug>/` (kebab-case, e.g. `mockups/permission-requests/`) so multiple explored features coexist. Never write mockup files into `mockups/` directly.

## Ticket-linked invocation

If the argument begins with an issue key followed by a colon (e.g. `SC-123: dark mode toggle`), the mockups belong to that ticket: use the lowercased key as the feature slug (`mockups/sc-123/`), use the text after the colon as the feature name, and add a top-level `"ticket": "SC-123"` field to `index.json`. An optional `Ticket context:` block after the first line is background for choosing options — never render it in the mockups. Without a leading key, behave exactly as described below.

## Variation invocation

When invoked with `--variation <KEY>: <feature>` followed by a block:

```
Vary this existing mockup:
  group slug: <parentSlug>
  source file: mockups/<parentSlug>/<sourceFile>
Write the new group to: mockups/<childSlug>/
Change instructions:
<free text>
```

produce a NEW group that iterates on ONE existing mockup rather than exploring fresh alternatives:

- **Read the source file first** and treat it as the hard baseline. Every option in the new group is a *refinement of that one mockup* applying the change instructions — NOT fresh unrelated alternatives. Preserve the source's interaction paradigm, sample data, and app-shell context; vary only what the instructions ask for (plus tightly related follow-on choices).
- **Never modify or delete the source group.** Write ONLY into the given `mockups/<childSlug>/` directory. The parent must remain viewable unchanged.
- Default to **3 variations** (the exploration is narrower than a first round); still render distinct, comparable options.
- Write `mockups/<childSlug>/index.json` with the normal fields PLUS `"parent": "<parentSlug>"`, `"parentFile": "<sourceFile>"`, `"instructions": "<free text>"`, and `"ticket": "<KEY>"` (the same ticket as the parent). Keep the same brief-bar / annotation / verify-before-presenting rules as a first-round set.

## Ground rules

1. **Match the real app.** Locate the project's actual frontend (stylesheet, design tokens, app shell markup) and reproduce its look faithfully: colors, type, chrome, layout. Every mockup renders the pattern inside the real visual context, never on a blank page. If the project has no existing UI or several, ask which surface the mockups target before starting.
2. **Same data everywhere.** Invent one small, realistic sample dataset that fits the product (real-looking ticket keys, names, timestamps) and render the IDENTICAL data in every option. Options must differ only in the interaction pattern, never in content.
3. **Genuinely distinct options.** Each file is a different interaction paradigm (e.g. blocking modal / notification stack / dedicated panel / inline-in-context / persistent strip), not styling variants of one idea. If two drafts feel similar, replace one.
4. **Self-contained files.** Inline all CSS in each file. No external resources, no imports — each file must render standalone from disk.

## Anatomy of each mockup file (`NN-short-name.html`)

- **Brief bar** above the app frame (clearly outside it): an eyebrow label (`<Feature> · Option N of M`), the option name as a one-line thesis, a short concept paragraph, pros/cons chips (green `+` / red `−`, mono font), and prev/next/index links.
- **App frame**: bordered, rounded, drop-shadowed reproduction of the app shell at a fixed width (~1240px), with the option rendered in place.
- **Annotation notes**: high-contrast sticky notes (amber works well on dark UIs; numbered chips; `ui-monospace`) absolutely positioned next to the UI they explain. Each note states BEHAVIOR — what happens, when, and which API/backend call powers it — not visual description. Notes must sit on empty areas, never covering the UI they point at.
- **Footer line**: "Static mockup — no functionality" plus the real data source / API verbs the pattern would use.

Also write, inside the feature subdirectory:

- `index.html`: linked cards for every option (name, one-liner, tag chips) and a closing hint on which options could combine.
- `index.json`: a machine-readable manifest so tools (e.g. an in-app mockup viewer) can list the set without parsing HTML:

```json
{
  "feature": "permission requests",
  "slug": "permission-requests",
  "created": "2026-07-11",
  "options": [
    {
      "n": 1,
      "name": "Blocking modal",
      "file": "01-modal.html",
      "description": "Takeover dialog, one request at a time; nothing else clickable until decided."
    }
  ]
}
```

One entry per option, in order; `description` is the option's one-line thesis from its brief bar. `ticket` is present only for ticket-linked invocations. Keep `index.json` in sync if options are added or revised.

## Verify before presenting

Render every file headless and LOOK at it — absolutely-positioned notes overlap content on the first try more often than not:

1. Screenshot each file with a headless browser, e.g. `chromium --headless --screenshot=NN.png --window-size=1400,1000 --hide-scrollbars file:///.../NN.html`. If only a sandboxed (Flatpak/Snap) browser is available, it may not read the project directory — copy the files to a directory it can access (e.g. `~/Downloads/tmp-mockcheck/`), render there, and delete the copy afterwards.
2. View each screenshot. Fix any note covering content, broken layout, or unreadable contrast; re-render until clean.

## Presenting

Summarize each option in one or two sentences with its sharpest pro and con, name the load-bearing differences, and offer a recommendation (including sensible combinations). Do NOT implement anything — the user picks first.

Note for the user: links between mockups only work in a browser that can see the whole directory. Sandboxed browsers opening a single file via the document portal will 404 on relative links — either grant the browser read access to `mockups/` or serve the directory with `python3 -m http.server`.
