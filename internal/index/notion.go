package index

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/gethuman-sh/human/internal/knowledge/notion"
)

// NotionClient is the subset of notion.Client methods needed by SyncNotion.
type NotionClient interface {
	Search(ctx context.Context, query string) ([]notion.SearchResult, error)
	GetPage(ctx context.Context, pageID string) (string, error)
	ListDatabases(ctx context.Context) ([]notion.DatabaseEntry, error)
	QueryDatabase(ctx context.Context, dbID string) ([]notion.DatabaseRow, error)
}

// NotionInstance holds a configured Notion workspace for indexing.
type NotionInstance struct {
	Name   string
	URL    string
	Client NotionClient
}

// SyncNotionResult summarises one Notion sync run.
type SyncNotionResult struct {
	Pages     int
	Databases int
	Pruned    int
	Errors    int
}

// SyncNotion iterates all Notion instances, discovers pages and databases,
// fetches content, upserts entries, and prunes stale keys.
func SyncNotion(ctx context.Context, store Store, instances []NotionInstance, logger io.Writer) (*SyncNotionResult, error) {
	result := &SyncNotionResult{}

	for i := range instances {
		inst := &instances[i]
		if err := syncNotionInstance(ctx, store, inst, logger, result); err != nil {
			_, _ = fmt.Fprintf(logger, "Error syncing Notion %s: %v\n", inst.Name, err)
			result.Errors++
		}
	}

	return result, nil
}

// syncNotionInstance syncs a single Notion workspace.
func syncNotionInstance(ctx context.Context, store Store, inst *NotionInstance, logger io.Writer, result *SyncNotionResult) error {
	results, err := inst.Client.Search(ctx, "")
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(logger, "Indexing Notion %s: %d objects found...\n", inst.Name, len(results))

	seen := make(map[string]bool)

	// Mark every discovered ID as seen BEFORE per-object fetch/upsert so
	// a transient fetch error cannot cause the prune step below to
	// delete the entry from the index.
	for _, sr := range results {
		if sr.Type == "page" || sr.Type == "database" {
			seen[sr.ID] = true
		}
	}

	for _, sr := range results {
		switch sr.Type {
		case "page":
			if err := syncNotionPage(ctx, store, inst, sr, logger); err != nil {
				result.Errors++
			} else {
				result.Pages++
			}
		case "database":
			if err := syncNotionDatabase(ctx, store, inst, sr, logger); err != nil {
				result.Errors++
			} else {
				result.Databases++
			}
		}
	}

	// Prune stale entries for this instance.
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

	return nil
}

func syncNotionPage(ctx context.Context, store Store, inst *NotionInstance, sr notion.SearchResult, logger io.Writer) error {
	md, err := inst.Client.GetPage(ctx, sr.ID)
	if err != nil {
		_, _ = fmt.Fprintf(logger, "  Error fetching page %s: %v\n", sr.ID, err)
		return err
	}
	entry := Entry{
		Key:     sr.ID,
		Source:  inst.Name,
		Kind:    "notion",
		Project: inst.Name,
		Title:   sr.Title,
		Status:  "page",
		URL:     sr.URL,
	}
	if err := store.UpsertEntry(ctx, entry, md); err != nil {
		_, _ = fmt.Fprintf(logger, "  Error indexing page %s: %v\n", sr.ID, err)
		return err
	}
	return nil
}

func syncNotionDatabase(ctx context.Context, store Store, inst *NotionInstance, sr notion.SearchResult, logger io.Writer) error {
	rows, err := inst.Client.QueryDatabase(ctx, sr.ID)
	if err != nil {
		_, _ = fmt.Fprintf(logger, "  Error querying database %s: %v\n", sr.ID, err)
		return err
	}
	desc := flattenDatabaseRows(rows)
	entry := Entry{
		Key:     sr.ID,
		Source:  inst.Name,
		Kind:    "notion",
		Project: inst.Name,
		Title:   sr.Title,
		Status:  "database",
		URL:     sr.URL,
	}
	if err := store.UpsertEntry(ctx, entry, desc); err != nil {
		_, _ = fmt.Fprintf(logger, "  Error indexing database %s: %v\n", sr.ID, err)
		return err
	}
	return nil
}

// flattenDatabaseRows converts database rows into a searchable text blob.
func flattenDatabaseRows(rows []notion.DatabaseRow) string {
	var parts []string
	for _, row := range rows {
		parts = append(parts, flattenProperties(row.Properties))
	}
	return strings.Join(parts, "\n")
}

// flattenProperties converts a property map into a searchable string.
func flattenProperties(props map[string]string) string {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		if v := props[k]; v != "" {
			parts = append(parts, k+": "+v)
		}
	}
	return strings.Join(parts, " | ")
}
