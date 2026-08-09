package config

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/gethuman-sh/human/errors"
)

// Target addresses one settable leaf in a document.
//
// It is deliberately about the FILE's shape — a section, maybe an entry within
// it, and a field — rather than about any schema. What a field means, which
// values it accepts and how it is spelled in a UI are questions for whoever
// holds the schema; where it lives in the file is this document's business, and
// keeping the two apart is what stops a second copy of the node plumbing
// growing wherever someone needs to write one value ([SC-3889]).
type Target struct {
	Section string
	// Scalar means the section IS the value: `project: cli`.
	Scalar bool
	// List means the section is a sequence of named entries.
	List bool
	// Instance addresses a list entry by its name: field. An unknown name
	// appends a new entry carrying that identity, so saving against a
	// not-yet-configured instance starts it rather than failing.
	Instance string
	// Index addresses a list entry by position instead; -1 to address by name.
	Index int
	// Field is the key within the section or the entry. Empty for a scalar
	// section.
	Field string
}

// Set writes one value, creating the section, the entry and the key as needed.
//
// Comments on the value being replaced are kept: someone wrote them about that
// setting, and a new value does not make them untrue.
func (d *Document) Set(t Target, value any) error {
	node, err := d.targetNode(t)
	if err != nil {
		return err
	}
	replaceNode(node, buildNode(value))
	return nil
}

func (d *Document) targetNode(t Target) (*yaml.Node, error) {
	mapping := d.mapping()
	if t.Scalar {
		return ensureMapValue(mapping, t.Section, yaml.ScalarNode), nil
	}
	if !t.List {
		section := ensureMapValue(mapping, t.Section, yaml.MappingNode)
		return ensureMapValue(section, t.Field, yaml.ScalarNode), nil
	}
	section := ensureMapValue(mapping, t.Section, yaml.SequenceNode)
	entry, err := d.listEntry(section, t)
	if err != nil {
		return nil, err
	}
	return ensureMapValue(entry, t.Field, yaml.ScalarNode), nil
}

func (d *Document) listEntry(section *yaml.Node, t Target) (*yaml.Node, error) {
	if t.Index >= 0 {
		if t.Index >= len(section.Content) {
			return nil, errors.WithDetails("list index out of range",
				"section", t.Section, "index", fmt.Sprintf("%d", t.Index), "entries", fmt.Sprintf("%d", len(section.Content)))
		}
		entry := section.Content[t.Index]
		if entry.Kind != yaml.MappingNode {
			return nil, errors.WithDetails("list entry is not a mapping", "section", t.Section)
		}
		return entry, nil
	}
	var found []*yaml.Node
	for _, entry := range section.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		if name := mapValue(entry, "name"); name != nil && name.Value == t.Instance {
			found = append(found, entry)
		}
	}
	switch len(found) {
	case 0:
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		entry.Content = append(entry.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t.Instance},
		)
		section.Content = append(section.Content, entry)
		return entry, nil
	case 1:
		return found[0], nil
	default:
		return nil, errors.WithDetails("duplicate instance name — edit the file directly",
			"section", t.Section, "name", t.Instance)
	}
}

// ensureMapValue returns the value node for key, appending a key/value pair of
// the wanted kind when absent. A null placeholder value (`key:`) is converted to
// the wanted kind rather than mutated into an odd empty scalar.
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

// buildNode renders a Go value as a yaml node.
func buildNode(value any) *yaml.Node {
	switch v := value.(type) {
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%t", v)}
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", v)}
	case []string:
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range v {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item})
		}
		return seq
	case []int64:
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range v {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", item)})
		}
		return seq
	default:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprintf("%v", v)}
	}
}

// replaceNode overwrites a node's value while keeping the comments written
// around it.
func replaceNode(dst, src *yaml.Node) {
	head, line, foot := dst.HeadComment, dst.LineComment, dst.FootComment
	*dst = *src
	dst.HeadComment, dst.LineComment, dst.FootComment = head, line, foot
}
