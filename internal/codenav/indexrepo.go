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
// project. When full is false and the source signature is unchanged since the
// last index, it skips the work and returns Skipped=true. Shared core behind
// `human codenav index` and the daemon's background indexer so the two paths
// never drift.
func IndexProject(ctx context.Context, st *store.Store, project, root string, full bool) (IndexResult, error) {
	scan := index.RepoScan{Project: project, Root: root}
	backends := index.PickFor(scan)
	if len(backends) == 0 {
		return IndexResult{}, errors.WithDetails("no indexer matched the repository", "root", root)
	}
	sig := index.SourceSignature(scan)
	if !full {
		if old, _ := st.ProjectSig(project); old != "" && old == sig {
			return IndexResult{Skipped: true}, nil
		}
	}
	w, err := st.NewWriter(project, root)
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
	if err := w.Commit(gitRev(root)); err != nil {
		return IndexResult{}, err
	}
	if err := st.SetProjectSig(project, sig); err != nil {
		return IndexResult{}, err
	}
	res := IndexResult{Elapsed: time.Since(start)}
	if projs, lerr := st.ListProjects(); lerr == nil {
		for _, p := range projs {
			if p.Name == project {
				res.Info = p
			}
		}
	}
	return res, nil
}

// gitRev returns the HEAD sha for root, or "" when root is not a git checkout.
func gitRev(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output() // #nosec G204 -- fixed git argv against a known repo root
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
