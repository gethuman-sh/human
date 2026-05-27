package cmdutil

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/tracker"
)

// AutoSaveTrackerConfig ensures the parsed tracker URL is represented in
// .humanconfig.yaml. If the config file doesn't exist, it creates one.
// If it exists, it appends the tracker entry if not already present.
func AutoSaveTrackerConfig(parsed *tracker.ParsedURL, configDir string) error {
	section, ok := tracker.KindToSection[parsed.Kind]
	if !ok {
		return errors.WithDetails("unknown tracker kind", "kind", parsed.Kind)
	}

	configFile := filepath.Join(configDir, ".humanconfig.yaml")

	// Check if the URL is already configured.
	var existing []struct {
		URL string `mapstructure:"url"`
	}
	_ = config.UnmarshalSection(configDir, section, &existing)
	for _, e := range existing {
		if urlsCompatible(e.URL, parsed.BaseURL) {
			return nil // Already configured.
		}
	}

	name := instanceNameFromURL(parsed)
	entry := buildYAMLEntry(parsed, name)

	// If file doesn't exist, create it with the section.
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		content := section + ":\n" + entry
		return os.WriteFile(configFile, []byte(content), 0o600)
	}

	// File exists — append the entry to the appropriate section.
	data, err := os.ReadFile(configFile) // #nosec G304 -- configFile is built from configDir parameter
	if err != nil {
		return errors.WrapWithDetails(err, "reading config file")
	}

	content := string(data)

	// Check if the section already exists in the file.
	sectionHeader := section + ":"
	if strings.Contains(content, sectionHeader) {
		// Append entry after the section header.
		content = strings.Replace(content, sectionHeader, sectionHeader+"\n"+entry, 1)
	} else {
		// Add new section at the end.
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + sectionHeader + "\n" + entry
	}

	return os.WriteFile(configFile, []byte(content), 0o644) // #nosec G306 -- .humanconfig.yaml is a project config file, not secrets
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

// buildYAMLEntry creates a YAML snippet for a tracker instance.
func buildYAMLEntry(parsed *tracker.ParsedURL, name string) string {
	var b strings.Builder
	b.WriteString("  - name: " + name + "\n")
	b.WriteString("    url: " + parsed.BaseURL + "\n")

	if parsed.Org != "" {
		b.WriteString("    org: " + parsed.Org + "\n")
	}

	return b.String()
}
