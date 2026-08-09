package cmdutil

import (
	"strings"

	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/tracker"
)

// AutoSaveTrackerConfig ensures the tracker behind a pasted URL is represented
// in .humanconfig, adding it when it is not.
//
// It goes through the config document, which is the only thing that should be
// editing this file. It used to append the entry by string surgery — finding
// the section header in the raw text and splicing YAML after it — which wrote
// the right thing often enough to look correct: a "jiras:" inside a comment was
// a valid splice point, an entry could land under the wrong section, and the
// shape written was always the per-vendor one whatever the file used
// ([SC-3889]).
func AutoSaveTrackerConfig(parsed *tracker.ParsedURL, configDir string) error {
	doc, err := config.Load(configDir)
	if err != nil {
		return err
	}
	for _, existing := range doc.Trackers() {
		if existing.Kind == parsed.Kind && urlsCompatible(existing.URL, parsed.BaseURL) {
			return nil // Already configured.
		}
	}

	entry := config.Tracker{
		Kind: parsed.Kind,
		Name: instanceNameFromURL(parsed),
		URL:  parsed.BaseURL,
		Org:  parsed.Org,
	}
	if err := doc.AddTracker(entry); err != nil {
		return err
	}
	return doc.Write()
}

// instanceNameFromURL derives a human-readable instance name from the parsed URL.
func instanceNameFromURL(parsed *tracker.ParsedURL) string {
	// For Atlassian Cloud: extract org from "org.atlassian.net".
	if strings.Contains(parsed.BaseURL, ".atlassian.net") {
		host := strings.TrimPrefix(parsed.BaseURL, "https://")
		host = strings.TrimPrefix(host, "http://")
		if idx := strings.Index(host, "."); idx > 0 {
			return host[:idx]
		}
	}

	// For Azure DevOps: use the org from ParsedURL.
	if parsed.Org != "" {
		return parsed.Org
	}

	// Default: use hostname without common prefixes/suffixes.
	host := strings.TrimPrefix(parsed.BaseURL, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.Split(host, ":")[0] // remove port

	// Remove common prefixes.
	for _, prefix := range []string{"api.", "app."} {
		host = strings.TrimPrefix(host, prefix)
	}

	// Use first segment of hostname.
	if idx := strings.Index(host, "."); idx > 0 {
		return host[:idx]
	}

	return host
}
