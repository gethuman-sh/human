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
func PruneStoppedMetas(now time.Time, retention time.Duration) ([]string, error) {
	metas, err := ListMetas()
	if err != nil {
		return nil, err
	}
	var removed []string
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
		if err := DeleteMeta(m.Name); err != nil {
			return removed, err
		}
		removed = append(removed, m.Name)
	}
	return removed, nil
}
