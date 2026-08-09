package devcontainer

import (
	"os"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gethuman-sh/human/errors"
)

// FileStore is the file access a Document needs. An interface so the wizard can
// pass its own writer and a test can pass an in-memory one.
type FileStore interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
}

// Document is one devcontainer.json being edited in place.
//
// Two places used to edit it — the wizard adding the human feature and the LSP
// step appending a post-start command — and both decoded it into a
// map[string]any and re-marshalled the whole file. Three things went wrong
// every time, none of them announced:
//
//   - devcontainer.json is JSONC. encoding/json rejects it outright, so the
//     feature writer FAILED on any file with a comment in it and the LSP writer
//     silently skipped one.
//   - MarshalIndent writes a map, so the file came back with its keys in
//     alphabetical order and every comment gone.
//   - postStartCommand is a string OR an array OR an object of named commands.
//     `raw["postStartCommand"].(string)` reads the last two as absent, and the
//     write then REPLACED the user's command with ours.
//
// Editing is surgical instead: the original bytes are kept and only the region
// being changed is spliced, so comments, key order and the user's own
// formatting all survive, and a shape this code cannot safely edit is reported
// rather than overwritten.
type Document struct {
	store   FileStore
	path    string
	content string
	changed bool
}

// LoadDocument reads path. A missing file is an error here — both callers only
// edit a devcontainer that already exists; the wizard writes a fresh one by a
// different path.
func LoadDocument(store FileStore, path string) (*Document, error) {
	data, err := store.ReadFile(path)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "reading devcontainer.json", "path", path)
	}
	if !gjson.Valid(string(StripJSONC(data))) {
		return nil, errors.WithDetails("devcontainer.json is not valid JSON", "path", path)
	}
	return &Document{store: store, path: path, content: string(data)}, nil
}

// Path is the file this document came from.
func (d *Document) Path() string { return d.path }

// Changed reports whether an edit altered the document.
func (d *Document) Changed() bool { return d.changed }

// HasFeature reports whether a feature is already declared.
func (d *Document) HasFeature(key string) bool {
	return gjson.Get(d.content, featurePath(key)).Exists()
}

// AddFeature declares a feature with no options, unless it is already declared.
func (d *Document) AddFeature(key string) error {
	if d.HasFeature(key) {
		return nil
	}
	updated, err := sjson.SetRaw(d.content, featurePath(key), "{}")
	if err != nil {
		return errors.WrapWithDetails(err, "adding devcontainer feature", "path", d.path, "feature", key)
	}
	d.content = updated
	d.changed = true
	return nil
}

// featurePath escapes a feature key for gjson/sjson, whose path syntax reads a
// dot as a level separator — and every feature key is a registry path full of
// them.
func featurePath(key string) string {
	escaped := make([]rune, 0, len(key)+8)
	for _, r := range key {
		if r == '.' || r == '*' || r == '?' || r == '\\' {
			escaped = append(escaped, '\\')
		}
		escaped = append(escaped, r)
	}
	return "features." + string(escaped)
}

// AppendPostStartCommand adds a shell command to postStartCommand under name,
// and reports whether the document now carries it.
//
// The three shapes the spec allows are handled as themselves rather than as a
// string with two wrong answers:
//
//   - absent: the command becomes postStartCommand.
//   - a string: joined with " && ", the shell's own sequencing.
//   - an object of named commands: added under its own name, which is exactly
//     what the object form is for.
//   - an array: refused. An array is ONE command's argv, not a list of them, so
//     there is nothing to append to without changing what the user's command
//     means. The caller is told, and the file is left alone — the old code read
//     this as absent and deleted it.
func (d *Document) AppendPostStartCommand(name, command string) error {
	existing := gjson.Get(d.content, "postStartCommand")

	switch {
	case !existing.Exists():
		return d.setPostStart(command)

	case existing.Type == gjson.String:
		if containsCommand(existing.String(), command) {
			return nil
		}
		return d.setPostStart(existing.String() + " && " + command)

	case existing.IsObject():
		for key, value := range existing.Map() {
			if key == name || containsCommand(value.String(), command) {
				return nil
			}
		}
		updated, err := sjson.Set(d.content, "postStartCommand."+name, command)
		if err != nil {
			return errors.WrapWithDetails(err, "adding named post-start command", "path", d.path, "name", name)
		}
		d.content = updated
		d.changed = true
		return nil

	default:
		return errors.WithDetails(
			"postStartCommand is an array — a single command's arguments, with nothing to append to",
			"path", d.path, "command", command,
			"hint", "make postStartCommand an object of named commands, then re-run")
	}
}

// containsCommand reports whether a command line already runs command, so
// running the step twice adds nothing.
func containsCommand(existing, command string) bool {
	return existing != "" && command != "" && strings.Contains(existing, command)
}

// setPostStart writes postStartCommand as a plain string.
func (d *Document) setPostStart(command string) error {
	updated, err := sjson.Set(d.content, "postStartCommand", command)
	if err != nil {
		return errors.WrapWithDetails(err, "setting post-start command", "path", d.path)
	}
	d.content = updated
	d.changed = true
	return nil
}

// Save writes the document back. An unchanged document is not written at all,
// so a devcontainer nobody edited keeps its own bytes exactly.
func (d *Document) Save() error {
	if !d.changed {
		return nil
	}
	content := d.content
	if content == "" || content[len(content)-1] != '\n' {
		content += "\n"
	}
	if err := d.store.WriteFile(d.path, []byte(content), 0o644); err != nil {
		return errors.WrapWithDetails(err, "writing devcontainer.json", "path", d.path)
	}
	return nil
}
