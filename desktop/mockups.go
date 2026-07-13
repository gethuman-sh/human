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

	"github.com/gethuman-sh/human/internal/daemon"
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
	Feature string         `json:"feature"`
	Slug    string         `json:"slug"`
	Created string         `json:"created"`
	Project string         `json:"project"`
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
