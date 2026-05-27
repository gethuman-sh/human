package cmdutil

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/tracker"
)

// WarnSkippedTrackers checks which trackers are configured in .humanconfig but
// did not produce loaded instances (typically due to missing credentials) and
// writes diagnostic messages to w.
// Returns true if any trackers were skipped.
func WarnSkippedTrackers(w io.Writer, dir string, loaded []tracker.Instance) bool {
	loadedSet := make(map[string]map[string]bool) // kind → set of names
	for _, inst := range loaded {
		if loadedSet[inst.Kind] == nil {
			loadedSet[inst.Kind] = make(map[string]bool)
		}
		loadedSet[inst.Kind][inst.Name] = true
	}

	statuses := tracker.DiagnoseTrackers(dir, config.UnmarshalSection, os.Getenv)

	anySkipped := false
	for _, ts := range statuses {
		if loadedSet[ts.Kind][ts.Name] {
			continue
		}

		anySkipped = true
		if len(ts.Missing) == 0 {
			// All creds seem present but instance still didn't load — generic message.
			_, _ = fmt.Fprintf(w, "Skipped %s/%s: credentials incomplete\n", ts.Kind, ts.Name)
		} else {
			_, _ = fmt.Fprintf(w, "Skipped %s/%s: missing %s\n", ts.Kind, ts.Name, strings.Join(ts.Missing, ", "))
		}
	}

	return anySkipped
}
