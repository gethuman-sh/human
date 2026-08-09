package config

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/gethuman-sh/human/errors"
)

// FileNames is viper's search order within one directory: extensions before the
// extensionless name, matching readConfig's SetConfigType("yaml"). A write must
// target the same file a read resolves, or an edit would shadow the live config
// with a second file.
var FileNames = []string{".humanconfig.yaml", ".humanconfig.yml", ".humanconfig"}

// LocateFile returns the config file this directory resolves to (dir first,
// then dir/local), or ("", false) when none exists.
func LocateFile(dir string) (string, bool) {
	for _, d := range []string{dir, filepath.Join(dir, "local")} {
		for _, name := range FileNames {
			p := filepath.Join(d, name)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p, true
			}
		}
	}
	return "", false
}

// Document is one .humanconfig file held whole: parsed, inspectable as typed
// entries, changeable through methods that say what they are for, checkable
// against itself, and writable back without losing a comment.
//
// It exists because the configuration had no object, and therefore nowhere to
// keep its rules ([SC-3889]). Reads went section by section through viper into
// seven independent per-provider structs; writes went leaf by leaf through a
// yaml node tree; the settings screen described the file a third time. No layer
// ever held "the configuration", so an invariant spanning two sections had no
// home — and the ones we needed ended up smuggled into a migration command and
// hand-hung on a provider's loader, where they could and did drift from the
// screen that offered the very values the loader rejected.
//
// The node tree is kept rather than re-rendered from the typed view on purpose.
// A config file is written by a person: it carries their comments, their
// ordering, and sections this binary has never heard of. Round-tripping through
// a struct would quietly discard all three, and a tool that eats your notes
// when you ask it to change one line has damaged the file it was asked to edit.
type Document struct {
	path   string
	exists bool
	root   *yaml.Node
}

// Load reads the config file dir resolves to. A missing file is not an error:
// it yields an empty document that Write will create, so a caller adding the
// first tracker to a fresh project takes the same path as one editing an
// existing file.
func Load(dir string) (*Document, error) {
	path, exists := LocateFile(dir)
	if !exists {
		path = filepath.Join(dir, FileNames[0])
	}
	doc := &Document{path: path, exists: exists}
	if !exists {
		doc.root = emptyRoot()
		return doc, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path resolved from the project's own config dir
	if err != nil {
		return nil, errors.WrapWithDetails(err, "reading config file", "file", path)
	}
	root, err := parseTree(data, path)
	if err != nil {
		return nil, err
	}
	doc.root = root
	return doc, nil
}

// Parse builds a document from bytes, for callers that already hold the content
// (and for tests, which should not need a temp directory to check a rule).
func Parse(data []byte, path string) (*Document, error) {
	root, err := parseTree(data, path)
	if err != nil {
		return nil, err
	}
	return &Document{path: path, exists: true, root: root}, nil
}

func parseTree(data []byte, path string) (*yaml.Node, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, errors.WrapWithDetails(err, "parsing config file", "file", path)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return emptyRoot(), nil
	}
	if root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.WithDetails("config root is not a mapping", "file", path)
	}
	return &root, nil
}

func emptyRoot() *yaml.Node {
	return &yaml.Node{
		Kind:    yaml.DocumentNode,
		Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
	}
}

// Path is the file this document was read from, or the file Write would create.
func (d *Document) Path() string { return d.path }

// Bytes renders the document as it would be written.
func (d *Document) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d.root); err != nil {
		return nil, errors.WrapWithDetails(err, "encoding config", "file", d.path)
	}
	if err := enc.Close(); err != nil {
		return nil, errors.WrapWithDetails(err, "encoding config", "file", d.path)
	}
	return buf.Bytes(), nil
}

// Write saves the document atomically, keeping the file's existing permissions.
//
// It deliberately does NOT validate first. A caller repairing a broken config
// has to be able to save an intermediate state, and a writer that refuses
// anything it disapproves of turns every rule into a wall. Validate is a
// separate question, asked by whoever wants the answer.
func (d *Document) Write() error {
	data, err := d.Bytes()
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if d.exists {
		if info, statErr := os.Stat(d.path); statErr == nil {
			perm = info.Mode().Perm()
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(d.path), ".humanconfig-*.tmp")
	if err != nil {
		return errors.WrapWithDetails(err, "creating temp config", "file", d.path)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errors.WrapWithDetails(err, "writing temp config", "file", d.path)
	}
	if err := tmp.Close(); err != nil {
		return errors.WrapWithDetails(err, "closing temp config", "file", d.path)
	}
	if err := os.Chmod(tmpName, perm); err != nil { // #nosec G703 -- temp file created above in the project's own config dir
		return errors.WrapWithDetails(err, "setting config permissions", "file", d.path)
	}
	if err := os.Rename(tmpName, d.path); err != nil { // #nosec G703 -- both paths derive from the project dir this document was located in
		return errors.WrapWithDetails(err, "replacing config file", "file", d.path)
	}
	d.exists = true
	return nil
}

// TrackerSections maps each issue-tracker section to the kind it configures.
// The document works in kinds; the sections are the file's spelling of them.
var TrackerSections = map[string]string{
	"jiras":       "jira",
	"githubs":     "github",
	"gitlabs":     "gitlab",
	"linears":     "linear",
	"shortcuts":   "shortcut",
	"azuredevops": "azuredevops",
	"clickups":    "clickup",
}

// ForgeSection is where code hosts are configured. A forge is not a tracker and
// does not appear above ([SC-3876]).
const ForgeSection = "forges"

// Tracker is one configured issue tracker, as the file declares it.
//
// It is a read-only view: changing a field changes nothing until it is handed
// back to a mutator. That is deliberate — the node tree is the truth, and a
// view that pretended otherwise would be a second model of the same file, which
// is the problem this type exists to end.
type Tracker struct {
	Section  string
	Kind     string
	Name     string
	Role     string
	Token    string
	URL      string
	Projects []string
}

// Forge is one configured code host.
type Forge struct {
	Name  string
	Kind  string
	URL   string
	Token string
}

// Trackers returns every configured issue tracker, in a stable order: sections
// alphabetically, entries in file order. Stability matters because callers
// compare and report these, and an order that shifted between runs would make a
// diff of two reports meaningless.
func (d *Document) Trackers() []Tracker {
	sections := make([]string, 0, len(TrackerSections))
	for section := range TrackerSections {
		sections = append(sections, section)
	}
	sort.Strings(sections)

	var out []Tracker
	for _, section := range sections {
		for _, entry := range d.entries(section) {
			out = append(out, Tracker{
				Section:  section,
				Kind:     TrackerSections[section],
				Name:     scalarAt(entry, "name"),
				Role:     scalarAt(entry, "role"),
				Token:    scalarAt(entry, "token"),
				URL:      scalarAt(entry, "url"),
				Projects: stringsAt(entry, "projects"),
			})
		}
	}
	return out
}

// Forges returns every configured code host, in file order.
func (d *Document) Forges() []Forge {
	var out []Forge
	for _, entry := range d.entries(ForgeSection) {
		kind := scalarAt(entry, "kind")
		if kind == "" {
			kind = "github"
		}
		out = append(out, Forge{
			Name:  scalarAt(entry, "name"),
			Kind:  kind,
			URL:   scalarAt(entry, "url"),
			Token: scalarAt(entry, "token"),
		})
	}
	return out
}

// AddTracker declares an issue tracker. The kind decides the section, so a
// caller says what it wants rather than where it goes.
//
// A name already present in that section is an error rather than an overwrite:
// two entries of one kind sharing a name make every by-name resolution
// ambiguous, and silently replacing someone's entry is not an addition.
func (d *Document) AddTracker(t Tracker) error {
	section := t.Section
	if section == "" {
		section = sectionForKind(t.Kind)
	}
	if section == "" {
		return errors.WithDetails("unknown tracker kind", "kind", t.Kind)
	}
	if t.Name == "" {
		return errors.WithDetails("a tracker needs a name", "section", section)
	}
	for _, existing := range d.entries(section) {
		if scalarAt(existing, "name") == t.Name {
			return errors.WithDetails("tracker already configured", "section", section, "name", t.Name)
		}
	}
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setScalar(entry, "name", t.Name)
	setScalar(entry, "url", t.URL)
	setScalar(entry, "token", t.Token)
	setScalar(entry, "role", t.Role)
	setStrings(entry, "projects", t.Projects)
	d.appendEntry(section, entry)
	return nil
}

// AddForge declares a code host.
func (d *Document) AddForge(f Forge) error {
	if f.Name == "" {
		return errors.WithDetails("a forge needs a name", "section", ForgeSection)
	}
	for _, existing := range d.entries(ForgeSection) {
		if scalarAt(existing, "name") == f.Name {
			return errors.WithDetails("forge already configured", "section", ForgeSection, "name", f.Name)
		}
	}
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setScalar(entry, "name", f.Name)
	if f.Kind != "" && f.Kind != "github" {
		setScalar(entry, "kind", f.Kind)
	}
	setScalar(entry, "url", f.URL)
	setScalar(entry, "token", f.Token)
	d.appendEntry(ForgeSection, entry)
	return nil
}

// RemoveTracker drops a tracker, reporting whether it was there. An emptied
// section is removed with it: an empty list reads as "configured, but broken".
func (d *Document) RemoveTracker(kind, name string) bool {
	return d.removeEntry(sectionForKind(kind), name)
}

// RemoveForge drops a code host, reporting whether it was there.
func (d *Document) RemoveForge(name string) bool {
	return d.removeEntry(ForgeSection, name)
}

// MoveTrackerToForge turns a tracker entry into a code-host entry: the node
// itself moves, so whatever the author wrote beside their token comes with it,
// and the fields a forge has no use for are dropped on the way.
//
// This is the shape of the config break that separated the two domains, and it
// is a method rather than a script inside one command because it is a fact
// about the document: these credentials describe a code host, not a backlog.
func (d *Document) MoveTrackerToForge(kind, name string) (bool, error) {
	section := sectionForKind(kind)
	if section == "" {
		return false, errors.WithDetails("unknown tracker kind", "kind", kind)
	}
	entry := d.findEntry(section, name)
	if entry == nil {
		return false, nil
	}
	for _, existing := range d.entries(ForgeSection) {
		if scalarAt(existing, "name") == name {
			// The credentials are already carried across, so the tracker entry is
			// a leftover and removing it deletes nothing ([SC-3887]).
			d.removeEntry(section, name)
			return true, nil
		}
	}
	keep := map[string]bool{"name": true, "kind": true, "url": true, "token": true}
	var content []*yaml.Node
	for i := 0; i+1 < len(entry.Content); i += 2 {
		if keep[entry.Content[i].Value] {
			content = append(content, entry.Content[i], entry.Content[i+1])
		}
	}
	entry.Content = content
	d.removeEntry(section, name)
	d.appendEntry(ForgeSection, entry)
	return true, nil
}

// --- node plumbing -------------------------------------------------------

func sectionForKind(kind string) string {
	for section, k := range TrackerSections {
		if k == kind {
			return section
		}
	}
	return ""
}

func (d *Document) mapping() *yaml.Node { return d.root.Content[0] }

// entries returns the mapping nodes of a list section, empty when the section
// is absent or is not a list.
func (d *Document) entries(section string) []*yaml.Node {
	node := mapValue(d.mapping(), section)
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	var out []*yaml.Node
	for _, entry := range node.Content {
		if entry.Kind == yaml.MappingNode {
			out = append(out, entry)
		}
	}
	return out
}

func (d *Document) findEntry(section, name string) *yaml.Node {
	for _, entry := range d.entries(section) {
		if scalarAt(entry, "name") == name {
			return entry
		}
	}
	return nil
}

func (d *Document) appendEntry(section string, entry *yaml.Node) {
	node := mapValue(d.mapping(), section)
	if node == nil {
		node = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		d.mapping().Content = append(d.mapping().Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: section}, node)
	}
	if node.Kind != yaml.SequenceNode {
		node.Kind, node.Tag, node.Value, node.Content = yaml.SequenceNode, "!!seq", "", nil
	}
	node.Content = append(node.Content, entry)
}

func (d *Document) removeEntry(section, name string) bool {
	node := mapValue(d.mapping(), section)
	if node == nil || node.Kind != yaml.SequenceNode {
		return false
	}
	var kept []*yaml.Node
	removed := false
	for _, entry := range node.Content {
		if entry.Kind == yaml.MappingNode && scalarAt(entry, "name") == name && !removed {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !removed {
		return false
	}
	node.Content = kept
	if len(kept) == 0 {
		removeKey(d.mapping(), section)
	}
	return true
}

func mapValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func removeKey(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

// scalarAt reads one scalar field from an entry, empty when absent.
func scalarAt(mapping *yaml.Node, key string) string {
	if v := mapValue(mapping, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

// stringsAt reads a string list field from an entry.
func stringsAt(mapping *yaml.Node, key string) []string {
	node := mapValue(mapping, key)
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind == yaml.ScalarNode {
			out = append(out, item.Value)
		}
	}
	return out
}

// setScalar appends a key/value pair, skipping empty values so a new entry
// carries only what was actually asked for rather than a row of blank keys.
func setScalar(entry *yaml.Node, key, value string) {
	if value == "" {
		return
	}
	entry.Content = append(entry.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func setStrings(entry *yaml.Node, key string, values []string) {
	if len(values) == 0 {
		return
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	entry.Content = append(entry.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, seq)
}
