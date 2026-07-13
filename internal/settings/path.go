package settings

import (
	"strconv"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// Ref is a fully resolved settings address.
type Ref struct {
	Group    *Group
	Field    *Field
	Instance string // name-based address; "" for singletons and scalars
	Index    int    // -1 unless bracket addressing was used
}

// ParsePath resolves a dotted path against the registry. Grammar:
//
//	project                  top-level scalar
//	vault.provider           singleton-group field
//	linears.work.projects    list group: <section>.<instanceName>.<field>
//	linears[1].token         index fallback for entries without a name
//
// List paths are parsed from the right — the final segment must be a known
// field key of the group — so instance names containing dots stay
// unambiguous (field keys are a closed set).
func ParsePath(path string) (Ref, error) {
	section, rest, bracketIndex, err := splitSection(path)
	if err != nil {
		return Ref{}, err
	}
	group, ok := groupBySection(section)
	if !ok {
		return Ref{}, errors.WithDetails("unknown settings section", "path", path, "section", section)
	}
	if group.Scalar {
		if rest != "" || bracketIndex >= 0 {
			return Ref{}, errors.WithDetails("scalar setting takes no field", "path", path)
		}
		return Ref{Group: group, Field: &group.Fields[0], Index: -1}, nil
	}
	if group.IsList {
		return parseListPath(group, path, rest, bracketIndex)
	}
	if bracketIndex >= 0 {
		return Ref{}, errors.WithDetails("index addressing on non-list section", "path", path, "section", section)
	}
	field, ok := fieldPtr(group, rest)
	if !ok {
		return Ref{}, errors.WithDetails("unknown settings field", "path", path, "section", section, "field", rest)
	}
	return Ref{Group: group, Field: field, Index: -1}, nil
}

// PathFor builds the canonical path for a leaf. Entries without a name fall
// back to index addressing so every leaf stays addressable.
func PathFor(g Group, instanceName string, index int, fieldKey string) string {
	if g.Scalar {
		return g.Section
	}
	if !g.IsList {
		return g.Section + "." + fieldKey
	}
	if instanceName == "" {
		return g.Section + "[" + strconv.Itoa(index) + "]." + fieldKey
	}
	return g.Section + "." + instanceName + "." + fieldKey
}

// splitSection separates the section head from the rest, handling the
// bracket index form. Returns bracketIndex -1 when not bracket-addressed.
func splitSection(path string) (section, rest string, bracketIndex int, err error) {
	if open := strings.IndexByte(path, '['); open >= 0 && (strings.IndexByte(path, '.') == -1 || open < strings.IndexByte(path, '.')) {
		closing := strings.IndexByte(path, ']')
		if closing < open {
			return "", "", -1, errors.WithDetails("malformed index address", "path", path)
		}
		idx, convErr := strconv.Atoi(path[open+1 : closing])
		if convErr != nil || idx < 0 {
			return "", "", -1, errors.WithDetails("malformed index address", "path", path)
		}
		rest := strings.TrimPrefix(path[closing+1:], ".")
		return path[:open], rest, idx, nil
	}
	section, rest, _ = strings.Cut(path, ".")
	return section, rest, -1, nil
}

func parseListPath(group *Group, path, rest string, bracketIndex int) (Ref, error) {
	if bracketIndex >= 0 {
		field, ok := fieldPtr(group, rest)
		if !ok {
			return Ref{}, errors.WithDetails("unknown settings field", "path", path, "section", group.Section, "field", rest)
		}
		return Ref{Group: group, Field: field, Index: bracketIndex}, nil
	}
	dot := strings.LastIndexByte(rest, '.')
	if dot < 0 {
		return Ref{}, errors.WithDetails("list setting needs <section>.<instance>.<field>", "path", path, "section", group.Section)
	}
	instance, fieldKey := rest[:dot], rest[dot+1:]
	field, ok := fieldPtr(group, fieldKey)
	if !ok {
		return Ref{}, errors.WithDetails("unknown settings field", "path", path, "section", group.Section, "field", fieldKey)
	}
	if instance == "" {
		return Ref{}, errors.WithDetails("empty instance name", "path", path, "section", group.Section)
	}
	return Ref{Group: group, Field: field, Instance: instance, Index: -1}, nil
}

func groupBySection(section string) (*Group, bool) {
	for si := range registry {
		for gi := range registry[si].Groups {
			if registry[si].Groups[gi].Section == section {
				return &registry[si].Groups[gi], true
			}
		}
	}
	return nil, false
}

func fieldPtr(g *Group, key string) (*Field, bool) {
	for i := range g.Fields {
		if g.Fields[i].Key == key {
			return &g.Fields[i], true
		}
	}
	return nil, false
}
