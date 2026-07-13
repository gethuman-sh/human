package settings

import (
	"fmt"

	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/vault"
)

// Masked is the placeholder returned for literal secrets. The frontend treats
// it as "unchanged" and SetValue rejects it as input, so a masked value can
// never round-trip back into the file.
const Masked = "••••••"

// Value is one flattened leaf: the palette index row and the form-field model.
type Value struct {
	Path            string    `json:"path"`
	Section         string    `json:"section"`
	SectionLabel    string    `json:"sectionLabel"`
	Group           string    `json:"group"`
	GroupLabel      string    `json:"groupLabel"`
	Kind            string    `json:"kind,omitempty"`
	Instance        string    `json:"instance,omitempty"`
	Field           string    `json:"field"`
	Label           string    `json:"label"`
	Type            FieldType `json:"type"`
	Enum            []string  `json:"enum,omitempty"`
	Value           any       `json:"value"`
	Masked          bool      `json:"masked,omitempty"`
	SecretRef       bool      `json:"secretRef,omitempty"`
	RestartRequired bool      `json:"restartRequired,omitempty"`
	ReadOnly        bool      `json:"readOnly,omitempty"`
	Description     string    `json:"description,omitempty"`
}

// Doc is the full settings snapshot for one project directory.
type Doc struct {
	Dir        string   `json:"dir"`
	ConfigFile string   `json:"configFile"` // resolved file path; "" when none exists yet
	Exists     bool     `json:"exists"`
	Values     []Value  `json:"values"`
	Warnings   []string `json:"warnings,omitempty"`
}

// Snapshot reads .humanconfig from dir and flattens every schema leaf.
// Secrets are never resolved: reads go through plain section unmarshalling so
// 1pw:// references stay references, and literal secrets are masked.
// A missing config file yields an editable skeleton (Exists=false) so the
// first save can create the file.
func Snapshot(dir string) (Doc, error) {
	file, exists := LocateConfigFile(dir)
	doc := Doc{Dir: dir, ConfigFile: file, Exists: exists}
	for _, sec := range Registry() {
		for gi := range sec.Groups {
			group := &sec.Groups[gi]
			values, warnings, err := groupValues(dir, sec, group)
			if err != nil {
				return Doc{}, err
			}
			doc.Values = append(doc.Values, values...)
			doc.Warnings = append(doc.Warnings, warnings...)
		}
	}
	return doc, nil
}

func groupValues(dir string, sec SectionDef, group *Group) ([]Value, []string, error) {
	switch {
	case group.Scalar:
		return []Value{leaf(sec, group, "", -1, group.Fields[0], config.ReadProjectName(dir))}, nil, nil
	case group.IsList:
		return listValues(dir, sec, group)
	default:
		var m map[string]any
		if err := config.UnmarshalSection(dir, group.Section, &m); err != nil {
			return nil, nil, err
		}
		values := make([]Value, 0, len(group.Fields))
		for _, f := range group.Fields {
			values = append(values, leaf(sec, group, "", -1, f, m[f.Key]))
		}
		return values, nil, nil
	}
}

func listValues(dir string, sec SectionDef, group *Group) ([]Value, []string, error) {
	var entries []map[string]any
	if err := config.UnmarshalSection(dir, group.Section, &entries); err != nil {
		return nil, nil, err
	}
	var values []Value
	var warnings []string
	seen := map[string]bool{}
	for i, entry := range entries {
		name := asString(entry["name"])
		// Duplicate names make name-based addressing ambiguous; the later
		// entry is surfaced read-only under index addressing instead of
		// silently shadowing the first.
		duplicate := name != "" && seen[name]
		if duplicate {
			warnings = append(warnings, fmt.Sprintf("duplicate %s instance name %q — later entry is read-only", group.Section, name))
		}
		if name != "" {
			seen[name] = true
		}
		addressName := name
		if duplicate {
			addressName = ""
		}
		for _, f := range group.Fields {
			v := leaf(sec, group, addressName, i, f, entry[f.Key])
			v.Instance = name
			v.ReadOnly = duplicate
			values = append(values, v)
		}
	}
	return values, warnings, nil
}

func leaf(sec SectionDef, group *Group, addressName string, index int, f Field, raw any) Value {
	v := Value{
		Path:            PathFor(*group, addressName, index, f.Key),
		Section:         sec.Key,
		SectionLabel:    sec.Label,
		Group:           group.Section,
		GroupLabel:      group.Label,
		Kind:            group.Kind,
		Field:           f.Key,
		Label:           f.Label,
		Type:            f.Type,
		Enum:            f.Enum,
		RestartRequired: f.RestartRequired,
		Description:     f.Description,
	}
	v.Value = normalize(f.Type, raw)
	if f.Type == TypeSecret {
		v.Value, v.Masked, v.SecretRef = maskSecret(asString(raw))
	}
	return v
}

// maskSecret keeps vault references verbatim (they are pointers, not
// secrets) and replaces literal values with the write-only sentinel.
func maskSecret(s string) (value any, masked, secretRef bool) {
	switch {
	case s == "":
		return "", false, false
	case vault.IsSecretRef(s):
		return s, false, true
	default:
		return Masked, true, false
	}
}

// normalize converts viper's loosely typed values into the JSON shape the
// field type promises, so the frontend can rely on it.
func normalize(t FieldType, raw any) any {
	switch t {
	case TypeBool:
		b, _ := raw.(bool)
		return b
	case TypeStringList:
		return asStringList(raw)
	case TypeIntList:
		return asIntList(raw)
	case TypeInt:
		n, _ := asInt64(raw)
		return n
	default:
		return asString(raw)
	}
}

func asString(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func asStringList(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]string); ok {
			return typed
		}
		return []string{}
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, asString(it))
	}
	return out
}

func asIntList(raw any) []int64 {
	items, ok := raw.([]any)
	if !ok {
		return []int64{}
	}
	out := make([]int64, 0, len(items))
	for _, it := range items {
		if n, ok := asInt64(it); ok {
			out = append(out, n)
		}
	}
	return out
}

func asInt64(raw any) (int64, bool) {
	switch v := raw.(type) {
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint64:
		return int64(v), true
	case float64:
		if v != float64(int64(v)) {
			return 0, false
		}
		return int64(v), true
	default:
		return 0, false
	}
}
