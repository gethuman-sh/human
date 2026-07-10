//go:build wailsapp

package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/gethuman-sh/human/errors"
)

// featureFile is the project-root document the Features view renders. It is read
// from disk at call time (not embedded) so it always reflects the current repo.
const featureFile = "FEATURE.json"

// FeatureItem is one feature: a short name and a one-line description. Recent
// flags a capability changed since the last release (for a "new" badge) and is
// omitempty so older flat FEATURE.json files still unmarshal cleanly. The
// document's per-feature ticket keys are intentionally not modelled here: the
// desktop pane presents features from a user's point of view, not their
// engineering trail, so the binding never carries them to the frontend
// (json.Unmarshal simply ignores the unmapped "tickets" field).
type FeatureItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Recent      bool   `json:"recent,omitempty"`
}

// FeatureGroup is a titled cluster of related features. Groups nests sub-groups
// for larger projects (the generator scales nesting depth to project size); it
// is omitempty so a flat, single-level document is equally valid.
type FeatureGroup struct {
	Group    string         `json:"group"`
	Features []FeatureItem  `json:"features"`
	Groups   []FeatureGroup `json:"groups,omitempty"`
}

// FeatureDoc is the full FEATURE.json payload the frontend renders. Error carries
// a read/parse failure to the view as an empty-state message rather than failing
// the binding, mirroring BoardData.Error's banner convention. Exists tells the
// pane whether FEATURE.json is present so its action button can read "Generate"
// (absent) vs "Refresh" (present) — a clean signal independent of Error, so a
// missing file is a friendly prompt rather than a red error banner.
type FeatureDoc struct {
	Product string         `json:"product"`
	Tagline string         `json:"tagline"`
	Groups  []FeatureGroup `json:"groups"`
	Exists  bool           `json:"exists"`
	Error   string         `json:"error,omitempty"`
}

// Features reads FEATURE.json from the project root and returns its grouped
// feature list for the desktop board's Features pane. Like Instances() it needs
// no daemon or credentials — it is a plain file read. A missing or malformed file
// is surfaced via FeatureDoc.Error (not a hard error) so the view can show an
// empty state instead of the app breaking.
func (a *App) Features() (FeatureDoc, error) {
	path, err := findUp(featureFile)
	if err != nil {
		// A missing file is not an error state — it is the "not generated yet"
		// case. Leave Error empty (Exists=false) so the pane shows a friendly
		// Generate prompt rather than a red banner.
		return FeatureDoc{Exists: false}, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is the fixed FEATURE.json resolved from the project tree
	if err != nil {
		return FeatureDoc{Error: errors.WrapWithDetails(err, "reading feature document", "path", path).Error()}, nil
	}
	var doc FeatureDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return FeatureDoc{Error: errors.WrapWithDetails(err, "parsing feature document", "path", path).Error()}, nil
	}
	doc.Exists = true
	return doc, nil
}

// findUp resolves name by walking up from the working directory to the
// filesystem root, so the Features view works whether the app is launched from
// the project root or a subdirectory. It returns an error (not a path) when the
// file is nowhere in the ancestry.
func findUp(name string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", errors.WrapWithDetails(err, "resolving working directory", "name", name)
	}
	for {
		candidate := filepath.Join(dir, name)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.WithDetails("feature document not found", "name", name)
		}
		dir = parent
	}
}
