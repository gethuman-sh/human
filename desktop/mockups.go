//go:build wailsapp

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/mockups"
)

// MockupOption and MockupSet mirror mockups/<slug>/index.json, the manifest
// the /human-mockups skill writes next to each set of static HTML mockups.
type MockupOption struct {
	N           int    `json:"n"`
	Name        string `json:"name"`
	File        string `json:"file"`
	Description string `json:"description"`
}

type MockupSet struct {
	Feature string `json:"feature"`
	Slug    string `json:"slug"`
	Created string `json:"created"`
	Project string `json:"project"`
	// Ticket is the PM key a ticket-linked invocation recorded in the
	// manifest — recovery metadata; the authoritative ticket→set link is the
	// project's .human/mockups.json.
	Ticket  string         `json:"ticket,omitempty"`
	Options []MockupOption `json:"options"`
}

// mockupDirs maps a set slug to its directory on disk. MockupSets rebuilds it
// on every scan; the asset-server middleware reads it to serve mockup files.
// Package-level (not on App) because the middleware is installed in main()
// before the App is bound.
var (
	mockupMu   sync.Mutex
	mockupDirs = map[string]string{}
)

// mockupRoots returns the project directories to scan for mockups/ — the
// daemon's registered projects when available, else the working directory so
// `wails dev` inside a project still finds its mockups without a daemon.
func mockupRoots() []daemon.ProjectInfo {
	if info, err := daemon.ReadInfo(); err == nil && len(info.Projects) > 0 {
		return info.Projects
	}
	if wd, err := os.Getwd(); err == nil {
		return []daemon.ProjectInfo{{Name: filepath.Base(wd), Dir: wd}}
	}
	return nil
}

// MockupSets scans every project root for mockups/<slug>/index.json and
// returns the parsed manifests, newest first. Directories without a valid
// manifest are skipped — the skill always writes one, and guessing at loose
// HTML files would put unlabeled content in the viewer.
func (a *App) MockupSets() ([]MockupSet, error) {
	sets := []MockupSet{}
	dirs := map[string]string{}
	for _, p := range mockupRoots() {
		entries, err := os.ReadDir(filepath.Join(p.Dir, "mockups"))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			setDir := filepath.Join(p.Dir, "mockups", e.Name())
			data, err := os.ReadFile(filepath.Join(setDir, "index.json"))
			if err != nil {
				continue
			}
			var set MockupSet
			if json.Unmarshal(data, &set) != nil || len(set.Options) == 0 {
				continue
			}
			if set.Slug == "" {
				set.Slug = e.Name()
			}
			set.Project = p.Name
			// Cross-project slug collision: first project wins. The slug is
			// the URL key, so a duplicate cannot be served unambiguously.
			if _, dup := dirs[set.Slug]; dup {
				continue
			}
			dirs[set.Slug] = setDir
			sets = append(sets, set)
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Created > sets[j].Created })

	mockupMu.Lock()
	mockupDirs = dirs
	mockupMu.Unlock()
	return sets, nil
}

// cardMockupInfo is one card's link to a local mockup set: the set slug plus
// whether the set is ready to view or still being generated.
type cardMockupInfo struct {
	Slug  string
	State string // "ready" | "creating"
}

// creatingWindow bounds how long a mockup link without a finished set counts
// as "creating". Past it the link is treated as stale (a crashed or abandoned
// agent) so the card offers creation again instead of blocking forever.
const creatingWindow = 30 * time.Minute

// cardMockups returns the ticket→mockup link for every registered project,
// read from each project's .human/mockups.json. A linked set only counts as
// ready when its index.json passes the same validity rule MockupSets applies
// (parses, has options) — so "View mocks" never points at a set the viewer
// will not list. First project wins per key, matching MockupSets' slug
// collision rule.
func cardMockups() map[string]cardMockupInfo {
	links := map[string]cardMockupInfo{}
	for _, p := range mockupRoots() {
		for key, entry := range mockups.NewStore(mockups.PathIn(p.Dir)).All() {
			if _, dup := links[key]; dup || entry.Slug == "" {
				continue
			}
			switch {
			case validMockupSet(filepath.Join(p.Dir, "mockups", entry.Slug)):
				links[key] = cardMockupInfo{Slug: entry.Slug, State: "ready"}
			case time.Since(entry.Created) < creatingWindow:
				links[key] = cardMockupInfo{Slug: entry.Slug, State: "creating"}
			}
		}
	}
	return links
}

// validMockupSet reports whether setDir holds a manifest the mockup viewer
// would accept — MockupSets' validity rule, factored for the card link check.
func validMockupSet(setDir string) bool {
	data, err := os.ReadFile(filepath.Join(setDir, "index.json")) // #nosec G304 — path derived from registered project dirs
	if err != nil {
		return false
	}
	var set MockupSet
	return json.Unmarshal(data, &set) == nil && len(set.Options) > 0
}

// mockupMiddleware serves /mockups/<slug>/<file> straight from the set
// directories discovered by MockupSets, so the webview can iframe mockups
// from disk — a freshly generated set appears on the next view refresh, and
// nothing ships inside the binary.
func mockupMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, "/mockups/")
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		slug, file, ok := strings.Cut(rest, "/")
		if !ok || file == "" {
			file = "index.html"
		}

		mockupMu.Lock()
		dir := mockupDirs[slug]
		mockupMu.Unlock()

		// A set directory is flat: only plain basenames are ever valid, which
		// also keeps traversal sequences from escaping the set directory.
		if dir == "" || file != filepath.Base(file) || strings.HasPrefix(file, ".") {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, file))
	})
}
