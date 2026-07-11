// Package starter detects directories that contain no project yet and
// scaffolds them from curated starter templates hosted on GitHub.
package starter

// Template is one scaffoldable starter project. Type/Language are the stable
// identifiers the frontend sends back to StartProject; the *Label fields are
// what the wizard displays. Path is the template's subdirectory inside the
// gethuman-sh/starters repo — backend-only, so it never leaks into the UI
// contract and the repo layout can change without a frontend release.
type Template struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	TypeLabel     string `json:"typeLabel"`
	Language      string `json:"language"`
	LanguageLabel string `json:"languageLabel"`
	Path          string `json:"-"`
}

// templates is the registry the wizard is generated from: the frontend derives
// its step-1 choices from the unique Types and step-2 from the Languages of
// the chosen Type, so adding a starter here is the only change needed to offer
// it in the app.
var templates = []Template{
	{
		ID:            "web-go",
		Type:          "web",
		TypeLabel:     "Web Project",
		Language:      "go",
		LanguageLabel: "Go",
		Path:          "web/go",
	},
}

// Templates returns the available starter templates. It returns a copy so
// callers cannot mutate the registry.
func Templates() []Template {
	out := make([]Template, len(templates))
	copy(out, templates)
	return out
}

// Lookup resolves the template for a type/language pair chosen in the wizard.
func Lookup(projectType, language string) (Template, bool) {
	for _, t := range templates {
		if t.Type == projectType && t.Language == language {
			return t, true
		}
	}
	return Template{}, false
}
