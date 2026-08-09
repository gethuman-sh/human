package recall

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// indexFetchCap bounds one listing. The index is after the entire record, so
// this sits far above any realistic backlog — a cap sized like a page would
// silently make "all past tickets" mean "the most recent page".
const indexFetchCap = 10000

// SyncResult summarises one sync run.
type SyncResult struct {
	Indexed int
	Pruned  int
	Errors  int
}

// Sync iterates all instances, lists issues per configured project,
// fetches descriptions, upserts entries, and prunes stale keys.
// When fullSync is false, it performs incremental sync using the last
// indexed timestamp per source to only fetch recently updated issues.
func Sync(ctx context.Context, store Store, instances []tracker.Instance, fullSync bool, logger io.Writer, opts ...Option) (*SyncResult, error) {
	result := &SyncResult{}
	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}

	for i := range instances {
		inst := &instances[i]
		if err := syncInstance(ctx, store, inst, fullSync, cfg.unattended, logger, result); err != nil {
			_, _ = fmt.Fprintf(logger, "Error syncing %s (%s): %v\n", inst.Name, inst.Kind, err)
			result.Errors++
		}
	}

	return result, nil
}

// syncInstance syncs a single tracker instance.
func syncInstance(ctx context.Context, store Store, inst *tracker.Instance, fullSync, unattended bool, logger io.Writer, result *SyncResult) error {
	seen := make(map[string]bool)

	// Determine if we can do incremental sync.
	lastIndexed, err := store.LastIndexedAt(ctx, inst.Name)
	if err != nil {
		return err
	}

	incremental := !fullSync && !lastIndexed.IsZero()

	if incremental {
		_, _ = fmt.Fprintf(logger, "Incremental sync for %s (%s) since %s\n", inst.Name, inst.Kind, lastIndexed.Format("2006-01-02 15:04:05"))
	} else {
		_, _ = fmt.Fprintf(logger, "Full sync for %s (%s)\n", inst.Name, inst.Kind)
	}

	// When projects are configured, sync each one; otherwise sync all projects at once.
	projects := inst.Projects
	if len(projects) == 0 {
		projects = []string{""}
	}

	// Truncation is per instance, not per project: one short listing makes the
	// whole instance's absence set unreliable.
	truncated := false
	for _, project := range projects {
		if syncProject(ctx, store, inst, project, fullSync, incremental, unattended, lastIndexed, logger, result, seen) {
			truncated = true
		}
	}

	// Only prune on full sync — incremental sync cannot detect deletions.
	if !incremental {
		if err := prune(ctx, store, inst, truncated, logger, result, seen); err != nil {
			return err
		}
	}

	return nil
}

// PruneBlastRadius is the share of an instance's indexed entries a single run
// may delete before the prune is refused.
var PruneBlastRadius = 0.2

// pruneFloor is the number of deletions always permitted regardless of share,
// so an ordinary small cleanup on a small index is not blocked by arithmetic.
const pruneFloor = 5

// prune removes entries whose tickets are gone upstream — and refuses when it
// cannot tell "gone" from "not fetched".
//
// This is the one operation here that destroys data. A listing that came back
// short because the backend capped it looks exactly like a backlog that was
// emptied, and pruning against it deletes the history it merely failed to
// fetch. Two independent guards, because one of them can be lied to:
//
//   - Truncation, when the backend reports it. Not every backend can: Shortcut
//     does not implement PagedLister, so it always reports "complete" whether or
//     not the server capped the response. A guard that trusts this alone is no
//     guard at all on the tracker that matters most.
//   - Blast radius, which rests on a fact about OUR data rather than a claim
//     from the provider. A run that would delete a large share of what we hold
//     is refused whatever the backend said.
//
// Refusing is cheap — the entries stay, slightly stale, and the next complete
// sync removes them. Deleting wrongly is not recoverable without a full
// re-index, and silently (SC-2132).
func prune(ctx context.Context, store Store, inst *tracker.Instance, truncated bool, logger io.Writer, result *SyncResult, seen map[string]bool) error {
	existingKeys, err := store.AllKeys(ctx, inst.Name)
	if err != nil {
		return err
	}
	var doomed []string
	for _, key := range existingKeys {
		if !seen[key] {
			doomed = append(doomed, key)
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	if truncated {
		_, _ = fmt.Fprintf(logger, "  Skipping prune for %s: the listing was truncated, so %d absent entries cannot be told from unfetched ones\n",
			inst.Name, len(doomed))
		return nil
	}
	if refusePrune(len(doomed), len(existingKeys)) {
		_, _ = fmt.Fprintf(logger, "  Skipping prune for %s: it would delete %d of %d entries, which looks like a short listing rather than deleted work\n",
			inst.Name, len(doomed), len(existingKeys))
		return nil
	}
	for _, key := range doomed {
		if err := store.DeleteEntry(ctx, key, inst.Name); err != nil {
			_, _ = fmt.Fprintf(logger, "  Error pruning %s: %v\n", key, err)
			result.Errors++
			continue
		}
		result.Pruned++
	}
	return nil
}

// refusePrune reports whether deleting doomed of existing entries is too large a
// share to be believable. Small absolute deletions are always allowed, so a
// genuine tidy-up on a small index is never blocked.
func refusePrune(doomed, existing int) bool {
	if doomed <= pruneFloor {
		return false
	}
	return float64(doomed) > PruneBlastRadius*float64(existing)
}

// detailFor returns the issue to index, re-fetching it only when the listing
// did not already carry what the index needs.
//
// The description is the whole reason for a second call — it is the full-text
// payload, and the field a slim list response omits. Some backends already
// return it (Shortcut's list carries Title, Status, Assignee, URL and
// Description), so re-fetching every listed issue there turned a one-call sync
// into N+1 for nothing. Others genuinely return slim payloads, which is why the
// detail panel re-fetches at all — so this asks only when the answer is missing
// rather than assuming either way (SC-2132).
func detailFor(ctx context.Context, p tracker.Provider, issue tracker.Issue) (*tracker.Issue, error) {
	if issue.Description != "" {
		return &issue, nil
	}
	return p.GetIssue(ctx, issue.Key)
}

// planBody returns the ticket's latest attached plan, or "" when it has none or
// the backend cannot serve comments.
//
// Best-effort by design: a ticket whose comments cannot be read is still worth
// indexing by title and description, and one unreadable thread must never cost
// the whole sync. A provider that is not a Commenter simply has no plans to
// find.
func planBody(ctx context.Context, p tracker.Provider, key string) string {
	commenter, ok := p.(tracker.Commenter)
	if !ok {
		return ""
	}
	comments, err := commenter.ListComments(ctx, key)
	if err != nil {
		return ""
	}
	m, found := marker.Latest(comments, "plan")
	if !found {
		return ""
	}
	return m.Body
}

// Option adjusts a sync. Variadic so the scheduled pass can say what it is
// without every hand-run caller and test restating a default.
type Option func(*options)

type options struct{ unattended bool }

// Unattended marks a sync as the daemon's scheduled pass rather than someone
// running `human index` and waiting. Backends may refuse work that is too
// expensive to do on a loop — an unscoped GitHub listing searches every issue
// the token can see ([SC-3888]) — and a person who asked for it explicitly is a
// different case from a timer asking every ten minutes.
func Unattended(o *options) { o.unattended = true }

// syncProject fetches and indexes issues for a single project (or all projects when project is "").
// syncProject reports whether the listing was truncated, which the caller needs
// before it may delete anything.
func syncProject(ctx context.Context, store Store, inst *tracker.Instance, project string, fullSync, incremental, unattended bool, lastIndexed time.Time, logger io.Writer, result *SyncResult, seen map[string]bool) bool {
	opts := tracker.ListOptions{
		Project:    project,
		Unattended: unattended,
		// The index wants the whole record, so the cap is set far above any real
		// backlog rather than at a page size. A backend that still cuts the list
		// short reports it through Truncated, which gates the prune.
		MaxResults: indexFetchCap,
		IncludeAll: fullSync,
	}
	if incremental {
		opts.UpdatedSince = lastIndexed
	}

	// ListIssuesPage rather than ListIssues: the plain call discards the
	// truncation signal, which is exactly what the prune needs to see.
	page, err := tracker.ListIssuesPage(ctx, inst.Provider, opts)
	if err != nil {
		_, _ = fmt.Fprintf(logger, "  Error listing %s/%s: %v\n", inst.Name, project, err)
		result.Errors++
		return false
	}
	issues := page.Issues

	label := project
	if label == "" {
		label = "(all projects)"
	}
	_, _ = fmt.Fprintf(logger, "Indexing %s (%s): %s (%d issues)...\n", inst.Name, inst.Kind, label, len(issues))

	// Mark every listed key as seen BEFORE per-issue fetch/upsert. A
	// transient fetch or upsert error must not cause the later prune
	// step to delete the entry from the index — the entry still exists
	// upstream and will be re-indexed on the next successful sync.
	for _, issue := range issues {
		seen[issue.Key] = true
	}

	for _, issue := range issues {
		full, fErr := detailFor(ctx, inst.Provider, issue)
		if fErr != nil {
			_, _ = fmt.Fprintf(logger, "  Error fetching %s: %v\n", issue.Key, fErr)
			result.Errors++
			continue
		}
		// Future PolicyProvider wrappers may return (nil, nil) to
		// indicate "not visible" — defend the deref below so a nil
		// issue never crashes the whole sync.
		if full == nil {
			_, _ = fmt.Fprintf(logger, "  Skipping %s: provider returned nil issue\n", issue.Key)
			continue
		}

		p := project
		if p == "" {
			p = full.Project
		}
		// Prefer the per-issue web URL populated by the provider; fall
		// back to the instance base URL when a provider does not set it.
		entryURL := full.URL
		if entryURL == "" {
			entryURL = inst.URL
		}
		// The plan is where a ticket says what it will CHANGE, which is the only
		// signal that connects two tickets describing one problem in different
		// words. Indexed as text so it is searchable, and its paths recorded so
		// "who else is changing this file" is an exact answer (SC-2132).
		plan := planBody(ctx, inst.Provider, issue.Key)
		entry := Entry{
			Key:      issue.Key,
			Source:   inst.Name,
			Kind:     inst.Kind,
			Project:  p,
			Title:    full.Title,
			Status:   full.Status,
			Assignee: full.Assignee,
			URL:      entryURL,
			Files:    ExtractFilePaths(plan),
		}
		if uErr := store.UpsertEntry(ctx, entry, full.Description+"\n"+plan); uErr != nil {
			_, _ = fmt.Fprintf(logger, "  Error indexing %s: %v\n", issue.Key, uErr)
			result.Errors++
			continue
		}
		result.Indexed++
	}
	return page.Truncated
}
