package index

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gethuman-sh/human/internal/tracker"
)

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
func Sync(ctx context.Context, store Store, instances []tracker.Instance, fullSync bool, logger io.Writer) (*SyncResult, error) {
	result := &SyncResult{}

	for i := range instances {
		inst := &instances[i]
		if err := syncInstance(ctx, store, inst, fullSync, logger, result); err != nil {
			_, _ = fmt.Fprintf(logger, "Error syncing %s (%s): %v\n", inst.Name, inst.Kind, err)
			result.Errors++
		}
	}

	return result, nil
}

// syncInstance syncs a single tracker instance.
func syncInstance(ctx context.Context, store Store, inst *tracker.Instance, fullSync bool, logger io.Writer, result *SyncResult) error {
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

	for _, project := range projects {
		syncProject(ctx, store, inst, project, fullSync, incremental, lastIndexed, logger, result, seen)
	}

	// Only prune on full sync — incremental sync cannot detect deletions.
	if !incremental {
		existingKeys, err := store.AllKeys(ctx, inst.Name)
		if err != nil {
			return err
		}
		for _, key := range existingKeys {
			if !seen[key] {
				if err := store.DeleteEntry(ctx, key, inst.Name); err != nil {
					_, _ = fmt.Fprintf(logger, "  Error pruning %s: %v\n", key, err)
					result.Errors++
					continue
				}
				result.Pruned++
			}
		}
	}

	return nil
}

// syncProject fetches and indexes issues for a single project (or all projects when project is "").
func syncProject(ctx context.Context, store Store, inst *tracker.Instance, project string, fullSync, incremental bool, lastIndexed time.Time, logger io.Writer, result *SyncResult, seen map[string]bool) {
	opts := tracker.ListOptions{
		Project:    project,
		MaxResults: 100,
		IncludeAll: fullSync,
	}
	if incremental {
		opts.UpdatedSince = lastIndexed
	}

	issues, err := inst.Provider.ListIssues(ctx, opts)
	if err != nil {
		_, _ = fmt.Fprintf(logger, "  Error listing %s/%s: %v\n", inst.Name, project, err)
		result.Errors++
		return
	}

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
		full, fErr := inst.Provider.GetIssue(ctx, issue.Key)
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
		entry := Entry{
			Key:      issue.Key,
			Source:   inst.Name,
			Kind:     inst.Kind,
			Project:  p,
			Title:    full.Title,
			Status:   full.Status,
			Assignee: full.Assignee,
			URL:      entryURL,
		}
		if uErr := store.UpsertEntry(ctx, entry, full.Description); uErr != nil {
			_, _ = fmt.Fprintf(logger, "  Error indexing %s: %v\n", issue.Key, uErr)
			result.Errors++
			continue
		}
		result.Indexed++
	}
}
