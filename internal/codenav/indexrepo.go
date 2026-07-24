package codenav

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/codenav/index"
	"github.com/gethuman-sh/human/internal/codenav/store"
)

// IndexResult reports the outcome of indexing one repository.
type IndexResult struct {
	Skipped bool              // source was byte-identical to the last index
	Info    store.ProjectInfo // populated when not skipped
	Elapsed time.Duration
}

// IndexProject indexes (or refreshes) the repository at root into st under
// project. The first index of a project and full=true take a complete rebuild;
// otherwise the refresh is incremental — it stats the working tree against the
// stored per-file fingerprints and reprocesses only added/modified/deleted files
// (Go at package granularity), leaving unchanged files in place. When nothing
// changed it returns Skipped=true. Shared core behind `human codenav index` and
// the daemon's background indexer so the two paths never drift.
func IndexProject(ctx context.Context, st *store.Store, project, root string, full bool) (IndexResult, error) {
	scan := index.RepoScan{Project: project, Root: root}
	backends := index.PickFor(scan)
	if len(backends) == 0 {
		return IndexResult{}, errors.WithDetails("no indexer matched the repository", "root", root)
	}
	exists, err := st.ProjectExists(project)
	if err != nil {
		return IndexResult{}, err
	}
	if full || !exists {
		return fullIndex(ctx, st, scan, backends)
	}
	delta, err := computeDelta(st, scan)
	if err != nil {
		return IndexResult{}, err
	}
	if delta.Empty() {
		return IndexResult{Skipped: true}, nil
	}
	return incrementalIndex(ctx, st, scan, backends, delta)
}

// fullIndex rebuilds the whole project: it clears any prior rows and runs every
// backend over the entire tree. Used for the first index and for --full.
func fullIndex(ctx context.Context, st *store.Store, scan index.RepoScan, backends []index.Indexer) (IndexResult, error) {
	w, err := st.NewWriter(scan.Project, scan.Root)
	if err != nil {
		return IndexResult{}, err
	}
	start := time.Now()
	for _, ix := range backends {
		if err := ix.Index(ctx, scan, w); err != nil {
			_ = w.Rollback()
			return IndexResult{}, err
		}
	}
	if err := w.Commit(gitRev(scan.Root)); err != nil {
		return IndexResult{}, err
	}
	return finishResult(st, scan.Project, start), nil
}

// incrementalIndex applies only the delta: it removes deleted files, then lets
// each backend reprocess the files it owns (falling back to a full backend run
// for any backend without incremental support). It falls back to a full rebuild
// if the project vanished between the existence check and here.
func incrementalIndex(ctx context.Context, st *store.Store, scan index.RepoScan, backends []index.Indexer, delta index.Delta) (IndexResult, error) {
	w, err := st.NewIncrementalWriter(scan.Project, scan.Root)
	if err != nil {
		return fullIndex(ctx, st, scan, backends)
	}
	start := time.Now()
	if err := w.RemoveFiles(delta.Deleted); err != nil {
		_ = w.Rollback()
		return IndexResult{}, err
	}
	// The Writer serves as PriorIndex: its reads go through the same transaction
	// (the pool holds a single connection, so a separate read would deadlock).
	for _, ix := range backends {
		if inc, ok := ix.(index.IncrementalIndexer); ok {
			err = inc.IndexIncremental(ctx, scan, delta, w, w)
		} else {
			err = ix.Index(ctx, scan, w)
		}
		if err != nil {
			_ = w.Rollback()
			return IndexResult{}, err
		}
	}
	if err := w.Commit(gitRev(scan.Root)); err != nil {
		return IndexResult{}, err
	}
	return finishResult(st, scan.Project, start), nil
}

// computeDelta diffs the working tree against the stored per-file fingerprints:
// a file absent from the store is Added; one whose size+mtime match is unchanged;
// otherwise its content is re-hashed and it is Modified only if the hash differs.
// Files left in the stored set are Deleted.
func computeDelta(st *store.Store, scan index.RepoScan) (index.Delta, error) {
	stored, err := st.ProjectFiles(scan.Project)
	if err != nil {
		return index.Delta{}, err
	}
	var delta index.Delta
	for _, f := range index.ScanSources(scan) {
		prev, ok := stored[f.Rel]
		if !ok {
			delta.Added = append(delta.Added, f.Rel)
			continue
		}
		delete(stored, f.Rel)
		if prev.Size == f.Size && prev.MTime == f.MTime {
			continue // unchanged per cheap stat
		}
		if index.HashFile(f.Abs) != prev.Hash {
			delta.Modified = append(delta.Modified, f.Rel)
		}
	}
	for path := range stored {
		delta.Deleted = append(delta.Deleted, path)
	}
	return delta, nil
}

// finishResult builds the IndexResult from the freshly committed project row.
func finishResult(st *store.Store, project string, start time.Time) IndexResult {
	res := IndexResult{Elapsed: time.Since(start)}
	if projs, lerr := st.ListProjects(); lerr == nil {
		for _, p := range projs {
			if p.Name == project {
				res.Info = p
			}
		}
	}
	return res
}

// gitRev returns the HEAD sha for root, or "" when root is not a git checkout.
func gitRev(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output() // #nosec G204 -- fixed git argv against a known repo root
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
