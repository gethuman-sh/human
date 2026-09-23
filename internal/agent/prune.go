package agent

import (
	"time"
)

// StoppedMetaRetention is how long an agent that has ended stays in the list.
// Long enough that a run from last week can still be found and its log
// read; short enough that the list says what the machine is doing, not what
// it did months ago (SC-5248).
const StoppedMetaRetention = 7 * 24 * time.Hour

// PruneStoppedMetas retires the records of agents that ended before
// now-retention and returns their names. A running record is never touched,
// whatever its age: the record is how a run is found again, and only the
// paths that stop a run may say it is over. A record that ended without a
// stop time is judged by when it started.
//
// Board agent names are deterministic and reused, so a stopped record can be
// relaunched under the same name between the ListMetas snapshot and the
// delete. Each candidate is re-read under its per-name lock — the same lock
// Stop/Delete hold — immediately before deleting it, so a relaunch that has
// already rewritten the record as running is seen and skipped rather than
// having its fresh metadata deleted out from under it.
//
// A delete failure for one candidate does not stop the sweep: the rest are
// still retired, and the last failure is reported so the caller can log it.
func PruneStoppedMetas(now time.Time, retention time.Duration) ([]string, error) {
	metas, err := ListMetas()
	if err != nil {
		return nil, err
	}
	var removed []string
	var lastErr error
	for _, m := range metas {
		if m.Status == StatusRunning {
			continue
		}
		ended := m.StoppedAt
		if ended.IsZero() {
			ended = m.CreatedAt
		}
		if now.Sub(ended) < retention {
			continue
		}
		deleted, err := deleteStoppedMetaLocked(m)
		if err != nil {
			lastErr = err
			continue
		}
		if !deleted {
			continue
		}
		removed = append(removed, m.Name)
	}
	return removed, lastErr
}

// deleteStoppedMetaLocked re-reads the named meta under its per-name lock and
// deletes it only if it is still the same stopped record the caller decided
// to retire. It reports whether the record was deleted.
func deleteStoppedMetaLocked(candidate Meta) (bool, error) {
	defer lockAgent(candidate.Name)()
	current, err := ReadMeta(candidate.Name)
	if err != nil {
		// Already gone, or unreadable: nothing left to race with a relaunch.
		return false, nil
	}
	if current.Status == StatusRunning || !current.StoppedAt.Equal(candidate.StoppedAt) {
		// A relaunch rewrote this record after the snapshot was taken.
		return false, nil
	}
	if err := DeleteMeta(candidate.Name); err != nil {
		return false, err
	}
	return true, nil
}
