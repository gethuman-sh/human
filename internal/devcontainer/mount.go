package devcontainer

import "strings"

// Mount is one bind mount: a host path made visible inside the container at a
// target path, with the options Docker takes after it.
//
// It exists because this package built "src:dst[:opts]" strings and then
// re-parsed what it had just built, in the same file, to deduplicate them by
// target. Two states that reachable code could reach:
//
//   - A Windows source path splits at its drive letter. `C:\repo:/workspace`
//     read as three colon-separated fields makes the TARGET `\repo`, so two
//     mounts to different places dedupe against each other and two to the same
//     place do not — and Docker answers the second case with "Duplicate mount
//     point".
//   - A string with no colon is dropped rather than kept. The dedupe skipped
//     anything it could not split into two fields, which removed it from the
//     result entirely instead of leaving it alone.
//
// Neither is reachable once mounts travel as fields. Parsing is confined to
// ParseBind, which exists only for the strings a project writes in its own
// devcontainer.json, and formatting happens once, at the Docker boundary.
type Mount struct {
	Source string
	Target string
	// Options are the flags Docker accepts after the target ("ro", "z",
	// "cached"). Kept verbatim rather than reduced to a read-only bool: a
	// project may name any of them, and modelling only the one this code
	// happens to set would silently drop what the project asked for.
	Options []string
}

// Bind returns a mount of source at target.
func Bind(source, target string, options ...string) Mount {
	return Mount{Source: source, Target: target, Options: options}
}

// ReadOnly returns the mount with the read-only flag set.
func (m Mount) ReadOnly() Mount {
	for _, o := range m.Options {
		if o == "ro" {
			return m
		}
	}
	m.Options = append(append([]string{}, m.Options...), "ro")
	return m
}

// String renders the Docker bind form. This is the only place a mount becomes
// a string: everything upstream compares and deduplicates by field.
func (m Mount) String() string {
	s := m.Source + ":" + m.Target
	if len(m.Options) > 0 {
		s += ":" + strings.Join(m.Options, ",")
	}
	return s
}

// ParseBind reads a Docker bind string. It reports false for anything that
// does not name both a source and a target, so a caller never has to decide
// what half a mount means.
//
// A leading Windows drive letter is not a field separator. Docker's own bind
// grammar treats `C:\path` as one path, and reading it as two fields is what
// made the target of a Windows mount its own directory name.
func ParseBind(s string) (Mount, bool) {
	fields := splitBindFields(s)
	if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
		return Mount{}, false
	}
	m := Mount{Source: fields[0], Target: fields[1]}
	if len(fields) > 2 && fields[2] != "" {
		m.Options = strings.Split(fields[2], ",")
	}
	return m, true
}

// splitBindFields splits a bind string into at most source, target and
// options, skipping a colon that belongs to a Windows drive letter.
func splitBindFields(s string) []string {
	var fields []string
	start := 0
	for i := 0; i < len(s) && len(fields) < 2; i++ {
		if s[i] != ':' {
			continue
		}
		if isDriveLetterColon(s, i, start) {
			continue
		}
		fields = append(fields, s[start:i])
		start = i + 1
	}
	return append(fields, s[start:])
}

// isDriveLetterColon reports whether the colon at i is the one in a Windows
// drive prefix — a single letter at the start of the field, followed by a
// path separator.
func isDriveLetterColon(s string, i, fieldStart int) bool {
	if i != fieldStart+1 {
		return false
	}
	c := s[fieldStart]
	if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
		return false
	}
	return i+1 < len(s) && (s[i+1] == '\\' || s[i+1] == '/')
}

// dedupeMounts keeps one mount per target, the last one wins, in the order
// those survivors appeared. A project's own mounts are appended after the
// programmatic ones precisely so they take precedence, and Docker refuses a
// create that names one target twice.
func dedupeMounts(mounts []Mount) []Mount {
	last := make(map[string]int, len(mounts))
	for i, m := range mounts {
		last[m.Target] = i
	}
	out := make([]Mount, 0, len(last))
	for i, m := range mounts {
		if last[m.Target] == i {
			out = append(out, m)
		}
	}
	return out
}

// bindStrings renders mounts for the Docker API.
func bindStrings(mounts []Mount) []string {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]string, len(mounts))
	for i, m := range mounts {
		out[i] = m.String()
	}
	return out
}
