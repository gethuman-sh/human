package config

import (
	"os"
	"path/filepath"
	"sync"
)

// UnmarshalSection reads a .humanconfig YAML file from dir and unmarshals
// the given key into target. Returns nil when the config file is missing.
//
// It is a thin call onto the parsed document now, rather than a second reader
// of the same file ([SC-3889]).
func UnmarshalSection(dir, key string, target any) error {
	doc, err := loadCached(dir)
	if err != nil || doc == nil {
		return err
	}
	return doc.DecodeSection(key, target)
}

// ReadProjectName reads the top-level "project" field from .humanconfig in dir.
// Returns "" if not set or config is missing.
func ReadProjectName(dir string) string {
	doc, err := loadCached(dir)
	if err != nil || doc == nil {
		return ""
	}
	return doc.String("project")
}

// BoardParticipates reports whether this machine should do autonomous board work
// (chaining reviews, re-driving loops, reclaiming stuck stages) for the project
// rooted at dir. Whether a machine participates in a project's work is its
// operator's decision, not a default that follows from being able to see the
// tracker (SC-2047). The knob is the "board.participate" field in .humanconfig;
// it DEFAULTS TO TRUE when unset or the config is missing, so a machine that says
// nothing keeps today's behaviour and only an explicit "board.participate: false"
// opts a registered project out of driving.
func BoardParticipates(dir string) bool {
	doc, err := loadCached(dir)
	if err != nil || doc == nil {
		return true
	}
	value, ok := doc.Bool("board.participate")
	if !ok {
		return true
	}
	return value
}

// ConfigFileNames are the accepted project-config filenames. Kept as an alias of
// the document's own list so the two can never disagree about which file is the
// config — they were separate lists, which is the same drift in miniature.
var ConfigFileNames = FileNames

// HasConfigFile reports whether dir directly contains one of the accepted
// .humanconfig filenames. Unlike Load it does not parse the file or search the
// local/ override — it is a cheap existence check for callers (the desktop
// app's "open project" picker) that must validate a user-chosen directory
// before treating it as a project root.
func HasConfigFile(dir string) bool {
	for _, name := range ConfigFileNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// Validate reports whether the .humanconfig file in dir (or its local/
// override) parses cleanly. Returns nil when dir holds no config file at all —
// there is nothing to validate — and an informative error when a file exists
// but fails to parse. Callers that must distinguish "no file" from "valid file"
// should pair this with HasConfigFile.
//
// This is the syntactic question. Document.Validate answers the other one:
// whether a file that parses says something sensible.
func Validate(dir string) error {
	_, err := loadCached(dir)
	return err
}

// docCache holds the parsed document per directory, keyed on what would make it
// stale.
//
// The file is small and read constantly: a single board refresh asks eight
// providers for their sections, each of which used to reopen and reparse it.
// Caching on (path, size, modtime) keeps one parse per edit rather than one per
// question, and a stat is cheap enough to do every time — which is what makes
// the cache safe to hold at all, since the file is edited by hand and by the
// settings screen while the daemon runs.
var docCache sync.Map // dir -> cachedDoc

type cachedDoc struct {
	stamp string
	doc   *Document
}

// loadCached returns the parsed document for dir, reparsing only when the file
// has changed. A missing config file yields (nil, nil): callers treat absence
// as "nothing configured", never as an error.
func loadCached(dir string) (*Document, error) {
	path, exists := LocateFile(dir)
	if !exists {
		return nil, nil
	}
	stamp := ""
	if info, err := os.Stat(path); err == nil {
		stamp = info.ModTime().UTC().String() + ":" + itoa(info.Size())
	}
	if cached, ok := docCache.Load(dir); ok {
		if entry, ok := cached.(cachedDoc); ok && entry.stamp == stamp && stamp != "" {
			return entry.doc, nil
		}
	}
	doc, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if stamp != "" {
		docCache.Store(dir, cachedDoc{stamp: stamp, doc: doc})
	}
	return doc, nil
}

// InvalidateCache drops the parsed document for dir. Write calls it, so a
// caller that edits and immediately re-reads sees its own change even inside
// the modtime's resolution.
func InvalidateCache(dir string) { docCache.Delete(dir) }

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
