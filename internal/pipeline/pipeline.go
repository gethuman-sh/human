// Package pipeline is the shared runtime for multi-agent scan pipelines
// (findbugs, security, gardening, brainstorm). The pipelines previously
// hand-rolled the same bookkeeping in every prompt: hidden handoff files,
// candidate IDs read from the last heading (racy under parallel agents),
// count files, duplicate filtering, timestamped reports, and cleanup lists.
// This package owns that mechanic; agents only contribute judgment.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// Workspace is one pipeline's scratch area under .human/<name>/.
type Workspace struct {
	// Dir is the project directory holding .human/.
	Dir string
	// Name is the pipeline name (bugs, security, gardening, brainstorms).
	Name string
}

// Finding is one appended candidate finding.
type Finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Category string `json:"category"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
}

// Open returns the workspace for name under dir, creating its directory.
func Open(dir, name string) (Workspace, error) {
	if !nameOK.MatchString(name) {
		return Workspace{}, errors.WithDetails("invalid pipeline name", "name", name)
	}
	w := Workspace{Dir: dir, Name: name}
	if err := os.MkdirAll(w.Root(), 0o750); err != nil {
		return Workspace{}, errors.WrapWithDetails(err, "creating pipeline workspace", "dir", w.Root())
	}
	return w, nil
}

var nameOK = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Root is the workspace directory, .human/<name>.
func (w Workspace) Root() string { return filepath.Join(w.Dir, ".human", w.Name) }

// CandidatesPath is the shared candidates file all finder agents append to.
func (w Workspace) CandidatesPath() string {
	return filepath.Join(w.Root(), "."+w.Name+"-candidates.md")
}

// StatePath is the shared key-value state file.
func (w Workspace) StatePath() string {
	return filepath.Join(w.Root(), "."+w.Name+"-state.md")
}

// lockPath guards candidate appends; mkdir is atomic on every platform.
func (w Workspace) lockPath() string {
	return filepath.Join(w.Root(), "."+w.Name+".lock")
}

// candidateHeading matches "### C-NNN: title" block headings.
var candidateHeading = regexp.MustCompile(`(?m)^### (C-\d+)`)

// locationLine matches the machine-readable location line of a finding block.
var locationLine = regexp.MustCompile(`(?m)^- location: (.+):(\d+) \((.+)\)$`)

// Append allocates the next candidate ID and appends the finding — unless an
// existing finding already claims the same file, line, and category, in which
// case the duplicate is dropped and the surviving ID returned. The whole
// read-allocate-write runs under a lock so parallel finder agents cannot race
// the ID sequence.
func (w Workspace) Append(f Finding) (id string, duplicate bool, err error) {
	unlock, err := w.lock()
	if err != nil {
		return "", false, err
	}
	defer unlock()

	existing, err := w.readCandidates()
	if err != nil {
		return "", false, err
	}

	if dupID := findDuplicate(existing, f); dupID != "" {
		return dupID, true, nil
	}

	id = nextID(existing)
	block := renderFinding(id, f)
	file, err := os.OpenFile(w.CandidatesPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", false, errors.WrapWithDetails(err, "opening candidates file", "path", w.CandidatesPath())
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(block); err != nil {
		return "", false, errors.WrapWithDetails(err, "appending finding", "path", w.CandidatesPath())
	}
	return id, false, nil
}

// Count returns the number of candidate findings.
func (w Workspace) Count() (int, error) {
	content, err := w.readCandidates()
	if err != nil {
		return 0, err
	}
	return len(candidateHeading.FindAllString(content, -1)), nil
}

// StateGet returns the value stored for key, or "" when unset.
func (w Workspace) StateGet(key string) (string, error) {
	content, err := readIfExists(w.StatePath())
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(content, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), key+": "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", nil
}

// StateSet stores key: value, replacing an existing entry for key.
func (w Workspace) StateSet(key, value string) error {
	if strings.ContainsAny(key, ":\n") || strings.Contains(value, "\n") {
		return errors.WithDetails("state keys and values must be single-line and colon-free", "key", key)
	}
	unlock, err := w.lock()
	if err != nil {
		return err
	}
	defer unlock()

	content, err := readIfExists(w.StatePath())
	if err != nil {
		return err
	}
	var lines []string
	replaced := false
	for line := range strings.SplitSeq(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key+": ") {
			if !replaced {
				lines = append(lines, key+": "+value)
				replaced = true
			}
			continue
		}
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if !replaced {
		lines = append(lines, key+": "+value)
	}
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(w.StatePath(), []byte(data), 0o600); err != nil {
		return errors.WrapWithDetails(err, "writing state file", "path", w.StatePath())
	}
	return nil
}

// ReportPath returns the timestamped final-report path for now, without
// creating the file — the triage agent writes the content.
func (w Workspace) ReportPath(now time.Time) string {
	return filepath.Join(w.Root(), w.Name+"-"+now.Format("20060102-150405")+".md")
}

// Cleanup removes every intermediate dot-file of the workspace, keeping final
// reports (which do not start with a dot).
func (w Workspace) Cleanup() error {
	entries, err := os.ReadDir(w.Root())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.WrapWithDetails(err, "reading pipeline workspace", "dir", w.Root())
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(w.Root(), entry.Name())); err != nil {
			return errors.WrapWithDetails(err, "removing pipeline scratch file", "file", entry.Name())
		}
	}
	return nil
}

// lock acquires the workspace lock, spinning briefly so parallel agents
// serialize instead of failing. mkdir is the portable atomic primitive.
func (w Workspace) lock() (func(), error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := os.Mkdir(w.lockPath(), 0o750)
		if err == nil {
			return func() { _ = os.Remove(w.lockPath()) }, nil
		}
		if !os.IsExist(err) {
			return nil, errors.WrapWithDetails(err, "acquiring pipeline lock", "path", w.lockPath())
		}
		if time.Now().After(deadline) {
			return nil, errors.WithDetails("pipeline lock held too long — remove it if a crashed agent left it behind", "path", w.lockPath())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (w Workspace) readCandidates() (string, error) {
	return readIfExists(w.CandidatesPath())
}

func readIfExists(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- workspace-internal path built from a validated pipeline name
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", errors.WrapWithDetails(err, "reading pipeline file", "path", path)
	}
	return string(data), nil
}

// findDuplicate returns the ID of an existing finding at the same file, line,
// and category — the mechanical duplicate rule; same-root-cause merging stays
// with the triage agent's judgment.
func findDuplicate(content string, f Finding) string {
	headings := candidateHeading.FindAllStringSubmatchIndex(content, -1)
	for i, h := range headings {
		end := len(content)
		if i+1 < len(headings) {
			end = headings[i+1][0]
		}
		block := content[h[0]:end]
		match := locationLine.FindStringSubmatch(block)
		if match == nil {
			continue
		}
		line, _ := strconv.Atoi(match[2])
		if match[1] == f.File && line == f.Line && match[3] == f.Category {
			return content[h[2]:h[3]]
		}
	}
	return ""
}

// nextID allocates the ID after the highest existing one, so IDs stay unique
// even when earlier findings were merged away.
func nextID(content string) string {
	max := 0
	for _, m := range candidateHeading.FindAllStringSubmatch(content, -1) {
		if n, err := strconv.Atoi(strings.TrimPrefix(m[1], "C-")); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("C-%03d", max+1)
}

func renderFinding(id string, f Finding) string {
	var b strings.Builder
	b.WriteString("\n### " + id + ": " + f.Title + "\n\n")
	b.WriteString("- location: " + f.File + ":" + strconv.Itoa(f.Line) + " (" + f.Category + ")\n")
	if f.Body != "" {
		b.WriteString("\n" + strings.TrimSpace(f.Body) + "\n")
	}
	return b.String()
}
