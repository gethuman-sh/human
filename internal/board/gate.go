// Package board holds the desktop workflow-board's pure, credential-free logic
// that must stay testable off the cgo (wailsapp) build path — chiefly the guard
// that decides when local view preferences may be pruned against a fetch.
package board

import (
	"fmt"
	"strings"

	"github.com/gethuman-sh/human/internal/daemon"
)

// Pruner is the subset of a local view-preferences store that board pruning
// drives. Both boardprefs.Store and ideaspace.Store satisfy it; each store is
// project-scoped, so pruning always names the project it applies to.
type Pruner interface {
	PruneExcept(project string, keys map[string]struct{}) error
}

// PruneTarget pairs a store with the key set to keep in it. Each store keeps a
// different set (all board cards vs. only idea-stage cards), so the keep set
// travels with its store rather than being shared.
type PruneTarget struct {
	Store Pruner
	Keep  map[string]struct{}
}

// FirstPMResult returns the first PM-role tracker result. v1 supports a single
// PM project; selecting by role mirrors the daemon's own role-based resolution.
func FirstPMResult(results []daemon.TrackerIssuesResult) (daemon.TrackerIssuesResult, bool) {
	for _, r := range results {
		if r.TrackerRole == "pm" {
			return r, true
		}
	}
	return daemon.TrackerIssuesResult{}, false
}

// PMRoleNotice explains an empty board that carries no PM-role tracker, so the
// UI can show why nothing renders instead of five silently empty columns — the
// SC-1655 defect, where a correctly configured non-Shortcut tracker returns
// issues that the board discards for lacking the pm role. Only Shortcut infers
// the pm role for free (see tracker.Instance.InferRole); every other kind needs
// an explicit `role: pm` in .humanconfig. When trackers were fetched but none
// carries the role, the message names them so the user knows which entry to
// annotate; with no trackers at all it gives the generic setup hint. An empty
// string means a PM-role result exists and the board renders normally.
func PMRoleNotice(results []daemon.TrackerIssuesResult) string {
	if _, ok := FirstPMResult(results); ok {
		return ""
	}
	var found []string
	for _, r := range results {
		// A tracker that errored is reported through its own error banner, not
		// as a role-misconfiguration hint — skip it so the notice names only
		// trackers that genuinely resolved without the pm role.
		if r.Err != "" {
			continue
		}
		found = append(found, fmt.Sprintf("%s (%s)", r.TrackerName, r.TrackerKind))
	}
	if len(found) == 0 {
		return "No PM-role tracker configured. Add role: pm to a tracker in .humanconfig so its issues appear on the board (see CLAUDE.md, \"Board rendering\")."
	}
	return fmt.Sprintf("No PM-role tracker configured. Found %s, but none has role: pm — add role: pm to one in .humanconfig so its issues appear on the board (see CLAUDE.md, \"Board rendering\").", strings.Join(found, ", "))
}

// CanPrune reports whether a fetch is trustworthy enough to prune local view
// preferences against. Pruning deletes every entry for a ticket absent from the
// fetch, so a degenerate fetch would erase everything the user saved. A
// token-less daemon returns a results slice with no PM-role entry at all, which
// is indistinguishable from — and must be treated no more destructively than —
// a tracker error. Permit pruning ONLY when a PM-role result is present, carried
// no error, and carried at least one issue.
func CanPrune(results []daemon.TrackerIssuesResult) bool {
	pm, ok := FirstPMResult(results)
	if !ok || pm.Err != "" {
		return false
	}
	return len(pm.Issues) > 0
}

// PrunePrefs drops stale entries from each target store for project, but ONLY
// when the fetch is trustworthy (CanPrune). A prune failure is ignored: a
// stale entry is harmless — the board renders from current cards regardless —
// whereas wiping everything on a degenerate fetch is the defect this guard
// exists to prevent.
func PrunePrefs(results []daemon.TrackerIssuesResult, project string, targets ...PruneTarget) {
	if !CanPrune(results) {
		return
	}
	for _, t := range targets {
		_ = t.Store.PruneExcept(project, t.Keep)
	}
}
