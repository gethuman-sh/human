package settings

import (
	"fmt"
	"slices"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
)

// LocateConfigFile returns the config file that dir resolves to, or ("", false)
// when none exists. It delegates: which file is the config is the config
// package's question, and the two lists this used to keep were one drift away
// from disagreeing ([SC-3889]).
func LocateConfigFile(dir string) (string, bool) {
	return config.LocateFile(dir)
}

// SetValue writes one settings leaf addressed by path.
//
// The schema decides what a path means and which values a field accepts; the
// document does the writing. That split is the point — this file used to carry
// its own copy of the yaml plumbing (loading, node addressing, atomic write),
// which is a second implementation of the file's shape sitting next to the
// first ([SC-3889]).
//
// Comments, key order and sections unknown to the schema survive the edit, as
// they always did: the document keeps the parsed tree rather than re-rendering
// it from a struct.
func SetValue(dir, path string, value any) error {
	ref, err := ParsePath(path)
	if err != nil {
		return err
	}
	coerced, err := coerceValue(ref.Field, value)
	if err != nil {
		return errors.WrapWithDetails(err, "invalid settings value", "path", path)
	}

	doc, err := config.Load(dir)
	if err != nil {
		return err
	}
	if err := doc.Set(targetFor(ref), coerced); err != nil {
		return errors.WrapWithDetails(err, "addressing settings key", "path", path, "file", doc.Path())
	}
	return doc.Write()
}

// targetFor translates a schema address into the document's own.
func targetFor(ref Ref) config.Target {
	target := config.Target{
		Section:  ref.Group.Section,
		Scalar:   ref.Group.Scalar,
		List:     ref.Group.IsList,
		Instance: ref.Instance,
		Index:    ref.Index,
	}
	if ref.Field != nil {
		target.Field = ref.Field.Key
	}
	return target
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
		// An unbounded field (both zero) keeps its historical behaviour. A
		// bounded one refuses a value its consumer would fall back away from,
		// so the save either takes effect or says why — except 0, which the
		// settings page sends when a row is cleared and which every bounded
		// consumer reads as "use the default".
		bounded := f.Min != 0 || f.Max != 0
		if bounded && n != 0 && (n < int64(f.Min) || n > int64(f.Max)) {
			return nil, errors.WithDetails("value out of range",
				"field", f.Key, "value", n, "min", f.Min, "max", f.Max)
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
	return slices.Contains(list, s)
}

// loadRoot parses the file into a document node, synthesizing an empty
// mapping document for a missing or empty file.
