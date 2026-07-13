package settings

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/gethuman-sh/human/errors"
)

// configFileNames is viper's search order within one directory: extensions
// before the extensionless name (matching internal/config.readConfig with
// SetConfigType("yaml")). Writes must target the same file reads see, or an
// edit would shadow the live config with a second file.
var configFileNames = []string{".humanconfig.yaml", ".humanconfig.yml", ".humanconfig"}

// LocateConfigFile returns the config file that internal/config's viper
// reader resolves for dir (dir first, then dir/local), or ("", false) when
// none exists.
func LocateConfigFile(dir string) (string, bool) {
	for _, d := range []string{dir, filepath.Join(dir, "local")} {
		for _, name := range configFileNames {
			p := filepath.Join(d, name)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p, true
			}
		}
	}
	return "", false
}

// SetValue writes one settings leaf addressed by path. The file is parsed
// into a yaml.v3 node tree and re-encoded, so comments, key order, and
// sections unknown to the schema survive the edit; only whitespace may
// normalize (2-space indent). Missing file, section, or named instance are
// created on the way. The write is atomic (temp file + rename).
func SetValue(dir, path string, value any) error {
	ref, err := ParsePath(path)
	if err != nil {
		return err
	}
	coerced, err := coerceValue(ref.Field, value)
	if err != nil {
		return errors.WrapWithDetails(err, "invalid settings value", "path", path)
	}

	file, exists := LocateConfigFile(dir)
	if !exists {
		file = filepath.Join(dir, configFileNames[0])
	}
	root, err := loadRoot(file, exists)
	if err != nil {
		return err
	}
	target, err := valueNodeFor(root, ref)
	if err != nil {
		return errors.WrapWithDetails(err, "addressing settings key", "path", path, "file", file)
	}
	replaceNode(target, buildNode(coerced))
	return writeAtomically(file, exists, root)
}

// coerceValue validates the JSON-shaped input against the field type and
// returns the canonical Go value the node builder understands.
func coerceValue(f *Field, value any) (any, error) {
	switch f.Type {
	case TypeSecret, TypeString, TypeEnum:
		return coerceString(f, value)
	case TypeBool:
		b, ok := value.(bool)
		if !ok {
			return nil, errors.WithDetails("expected bool", "field", f.Key)
		}
		return b, nil
	case TypeInt:
		n, ok := coerceInt(value)
		if !ok {
			return nil, errors.WithDetails("expected integer", "field", f.Key)
		}
		return n, nil
	case TypeStringList:
		return coerceStringList(f, value)
	case TypeIntList:
		return coerceIntList(f, value)
	default:
		return nil, errors.WithDetails("unknown field type", "field", f.Key, "type", string(f.Type))
	}
}

func coerceString(f *Field, value any) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", errors.WithDetails("expected string", "field", f.Key)
	}
	// The masked sentinel is a display placeholder; writing it back would
	// destroy the stored secret.
	if f.Type == TypeSecret && s == Masked {
		return "", errors.WithDetails("refusing to write masked placeholder", "field", f.Key)
	}
	if f.Type == TypeEnum && s != "" && !contains(f.Enum, s) {
		return "", errors.WithDetails("value not in enum", "field", f.Key, "value", s, "allowed", fmt.Sprintf("%v", f.Enum))
	}
	return s, nil
}

func coerceStringList(f *Field, value any) ([]string, error) {
	switch v := value.(type) {
	case []string:
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, it := range v {
			s, ok := it.(string)
			if !ok {
				return nil, errors.WithDetails("expected list of strings", "field", f.Key)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, errors.WithDetails("expected list of strings", "field", f.Key)
	}
}

func coerceIntList(f *Field, value any) ([]int64, error) {
	switch v := value.(type) {
	case []int64:
		return v, nil
	case []any:
		out := make([]int64, 0, len(v))
		for _, it := range v {
			n, ok := coerceInt(it)
			if !ok {
				return nil, errors.WithDetails("expected list of integers", "field", f.Key)
			}
			out = append(out, n)
		}
		return out, nil
	default:
		return nil, errors.WithDetails("expected list of integers", "field", f.Key)
	}
}

// coerceInt accepts the numeric shapes JSON decoding produces. Fractional
// values are rejected rather than truncated.
func coerceInt(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		if v != float64(int64(v)) {
			return 0, false
		}
		return int64(v), true
	default:
		return 0, false
	}
}

func contains(list []string, s string) bool {
	for _, it := range list {
		if it == s {
			return true
		}
	}
	return false
}

// loadRoot parses the file into a document node, synthesizing an empty
// mapping document for a missing or empty file.
func loadRoot(file string, exists bool) (*yaml.Node, error) {
	if !exists {
		return emptyDocument(), nil
	}
	data, err := os.ReadFile(file) // #nosec G304 -- path derived from the project's own config dir
	if err != nil {
		return nil, errors.WrapWithDetails(err, "reading config file", "file", file)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, errors.WrapWithDetails(err, "parsing config file", "file", file)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return emptyDocument(), nil
	}
	if root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.WithDetails("config root is not a mapping", "file", file)
	}
	return &root, nil
}

func emptyDocument() *yaml.Node {
	return &yaml.Node{
		Kind:    yaml.DocumentNode,
		Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
	}
}

// valueNodeFor navigates (and creates, where allowed) the node the ref
// addresses and returns the value node to overwrite.
func valueNodeFor(root *yaml.Node, ref Ref) (*yaml.Node, error) {
	mapping := root.Content[0]
	if ref.Group.Scalar {
		return ensureMapValue(mapping, ref.Group.Section, yaml.ScalarNode), nil
	}
	if !ref.Group.IsList {
		section := ensureMapValue(mapping, ref.Group.Section, yaml.MappingNode)
		return ensureMapValue(section, ref.Field.Key, yaml.ScalarNode), nil
	}
	section := ensureMapValue(mapping, ref.Group.Section, yaml.SequenceNode)
	entry, err := listEntry(section, ref)
	if err != nil {
		return nil, err
	}
	return ensureMapValue(entry, ref.Field.Key, yaml.ScalarNode), nil
}

func listEntry(section *yaml.Node, ref Ref) (*yaml.Node, error) {
	if ref.Index >= 0 {
		if ref.Index >= len(section.Content) {
			return nil, errors.WithDetails("list index out of range", "section", ref.Group.Section, "index", fmt.Sprintf("%d", ref.Index), "entries", fmt.Sprintf("%d", len(section.Content)))
		}
		entry := section.Content[ref.Index]
		if entry.Kind != yaml.MappingNode {
			return nil, errors.WithDetails("list entry is not a mapping", "section", ref.Group.Section)
		}
		return entry, nil
	}
	var found []*yaml.Node
	for _, entry := range section.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		if name := mapValue(entry, "name"); name != nil && name.Value == ref.Instance {
			found = append(found, entry)
		}
	}
	switch len(found) {
	case 0:
		// Unknown instance: append a fresh entry carrying its identity so a
		// save against a not-yet-configured name starts the entry.
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		entry.Content = append(entry.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: ref.Instance},
		)
		section.Content = append(section.Content, entry)
		return entry, nil
	case 1:
		return found[0], nil
	default:
		return nil, errors.WithDetails("duplicate instance name — edit the file directly", "section", ref.Group.Section, "name", ref.Instance)
	}
}

// mapValue returns the value node for key, or nil.
func mapValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// ensureMapValue returns the value node for key, appending a key/value pair
// of the wanted kind when absent. A null placeholder value (`key:`) is
// converted to the wanted kind rather than mutated into an odd empty scalar.
func ensureMapValue(mapping *yaml.Node, key string, want yaml.Kind) *yaml.Node {
	if v := mapValue(mapping, key); v != nil {
		if v.Tag == "!!null" && want != yaml.ScalarNode {
			resetNode(v, want)
		}
		return v
	}
	value := &yaml.Node{Kind: want}
	resetNode(value, want)
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
	return value
}

func resetNode(n *yaml.Node, kind yaml.Kind) {
	n.Kind = kind
	n.Value = ""
	n.Content = nil
	switch kind {
	case yaml.MappingNode:
		n.Tag = "!!map"
	case yaml.SequenceNode:
		n.Tag = "!!seq"
	default:
		n.Tag = "!!str"
	}
	n.Style = 0
}

// buildNode renders a coerced Go value as a yaml node.
func buildNode(value any) *yaml.Node {
	switch v := value.(type) {
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%t", v)}
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", v)}
	case []string:
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, it := range v {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: it})
		}
		return seq
	case []int64:
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, it := range v {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", it)})
		}
		return seq
	default:
		// coerceValue guarantees the cases above; this is a programming error.
		panic(fmt.Sprintf("settings: unsupported node value %T", value))
	}
}

// replaceNode swaps dst's content for src's while keeping the comments that
// hang on dst, so an inline comment on the old value survives the edit.
func replaceNode(dst, src *yaml.Node) {
	head, line, foot := dst.HeadComment, dst.LineComment, dst.FootComment
	*dst = *src
	dst.HeadComment, dst.LineComment, dst.FootComment = head, line, foot
}

func writeAtomically(file string, exists bool, root *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return errors.WrapWithDetails(err, "encoding config", "file", file)
	}
	if err := enc.Close(); err != nil {
		return errors.WrapWithDetails(err, "encoding config", "file", file)
	}

	perm := os.FileMode(0o644)
	if exists {
		if info, err := os.Stat(file); err == nil {
			perm = info.Mode().Perm()
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".humanconfig-*.tmp")
	if err != nil {
		return errors.WrapWithDetails(err, "creating temp config", "file", file)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return errors.WrapWithDetails(err, "writing temp config", "file", file)
	}
	if err := tmp.Close(); err != nil {
		return errors.WrapWithDetails(err, "closing temp config", "file", file)
	}
	if err := os.Chmod(tmpName, perm); err != nil { // #nosec G703 -- temp file created above in the project's own config dir
		return errors.WrapWithDetails(err, "setting config permissions", "file", file)
	}
	if err := os.Rename(tmpName, file); err != nil { // #nosec G703 -- both paths derive from the daemon-registered project dir
		return errors.WrapWithDetails(err, "replacing config file", "file", file)
	}
	return nil
}
