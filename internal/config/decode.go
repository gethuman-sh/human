package config

import (
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"gopkg.in/yaml.v3"

	"github.com/gethuman-sh/human/errors"
)

// DecodeSection decodes one top-level section into target.
//
// This is the read path, and it runs on the same parsed document every other
// question is asked of. It used to be a second reader: viper opened the file
// again for every section, so a single board refresh parsed one small file
// dozens of times and — more to the point — the typed view and the editable
// view were two different things that could disagree about what the file said
// ([SC-3889]).
//
// The decoder deliberately mimics the one it replaced: weakly typed, with the
// duration and comma-separated-slice hooks. A config that loaded yesterday must
// load today, including the sloppy shapes people actually write — a port number
// quoted as a string, a bool spelled "yes".
func (d *Document) DecodeSection(section string, target any) error {
	node := mapValue(d.mapping(), section)
	if node == nil {
		return nil
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return errors.WrapWithDetails(err, "parsing config section", "section", section, "file", d.path)
	}
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           target,
		WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return errors.WrapWithDetails(err, "building config decoder", "section", section)
	}
	if err := decoder.Decode(raw); err != nil {
		return errors.WrapWithDetails(err, "parsing config section", "section", section, "file", d.path)
	}
	return nil
}

// String reads a top-level scalar, empty when absent.
func (d *Document) String(key string) string {
	return scalarAt(d.mapping(), key)
}

// Bool reads a dotted path (e.g. "board.participate"), reporting whether it was
// set at all. The caller decides what an unset value means: some knobs default
// on, some off, and a reader that could not tell the difference would force
// every default to be true.
func (d *Document) Bool(path string) (value, ok bool) {
	node := d.mapping()
	parts := strings.Split(path, ".")
	for i, part := range parts {
		next := mapValue(node, part)
		if next == nil {
			return false, false
		}
		if i == len(parts)-1 {
			if next.Kind != yaml.ScalarNode {
				return false, false
			}
			var b bool
			if err := next.Decode(&b); err != nil {
				return false, false
			}
			return b, true
		}
		if next.Kind != yaml.MappingNode {
			return false, false
		}
		node = next
	}
	return false, false
}
