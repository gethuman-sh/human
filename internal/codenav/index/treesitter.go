package index

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// TreeSitter is the heuristic multi-language backend. It parses non-Go source
// with the pure-Go gotreesitter runtime, extracts definitions and call sites
// via bundled tags.scm queries, and resolves call sites to definitions with a
// name/scope cascade (see resolve.go). Fidelity is Heuristic — unlike GoNative,
// edges are name-resolved, not type-resolved.
//
// Only a curated, validated set of languages is enabled (gotreesitter ships 200+
// grammars but tags-query quality varies; these were verified to extract both
// definitions and references). Go is intentionally excluded — GoNative owns it.
type TreeSitter struct{}

var tsLangs = map[string]bool{
	"python":     true,
	"typescript": true,
	"javascript": true,
	"java":       true,
	"rust":       true,
}

// skipDirs are never descended into during the file walk.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, "__pycache__": true, ".venv": true,
	"venv": true, ".idea": true, ".vscode": true, ".codenav": true,
}

const maxFileBytes = 1 << 20 // skip files larger than 1 MiB

func (TreeSitter) Name() string       { return "treesitter" }
func (TreeSitter) Fidelity() Fidelity { return Heuristic }

// CanHandle always returns true: the backend contributes whatever curated
// non-Go files it finds, and emits nothing for repos without them.
func (TreeSitter) CanHandle(RepoScan) bool { return true }

type tsDef struct {
	name, qname, kind  string
	startByte, endByte uint32
	line, col          int
	signature          string
}

type tsRef struct {
	name               string
	startByte, endByte uint32
	line, col          int
}

type tsFile struct {
	rel  string
	defs []tsDef
	refs []tsRef
}

func (TreeSitter) Index(ctx context.Context, scan RepoScan, sink Sink) error {
	var files []tsFile
	nameToQNames := map[string][]string{} // bare name -> defining qnames (project-wide)

	// Pass A: parse every curated file once, emit File + Symbol, collect tags.
	walkErr := filepath.WalkDir(scan.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable entries
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if tf, ok := scanTSFile(scan.Root, path, d, sink, nameToQNames); ok {
			files = append(files, tf)
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}

	// Pass B: resolve each call site to a definition and emit edges + references.
	emitTSEdges(files, nameToQNames, sink)
	return nil
}

// scanTSFile parses one curated source file, emits its File and Symbol records,
// and returns the collected tags. ok is false for files the backend skips
// (wrong language, too large, unreadable, or outside the repo).
func scanTSFile(root, path string, d fs.DirEntry, sink Sink, nameToQNames map[string][]string) (tsFile, bool) {
	entry := grammars.DetectLanguage(d.Name())
	if entry == nil || !tsLangs[entry.Name] {
		return tsFile{}, false
	}
	info, err := d.Info()
	if err != nil || info.Size() > maxFileBytes {
		return tsFile{}, false
	}
	src, err := os.ReadFile(path) // #nosec G304 -- reads a source file discovered under the repo root
	if err != nil {
		return tsFile{}, false
	}
	rel, ok := relWithin(root, path)
	if !ok {
		return tsFile{}, false
	}
	tagger, err := gts.NewTagger(entry.Language(), grammars.ResolveTagsQuery(*entry))
	if err != nil {
		return tsFile{}, false
	}

	_ = sink.File(FileRec{Path: rel, Lang: entry.Name, ContentHash: hashFile(path), Fidelity: Heuristic})

	tf := tsFile{rel: rel}
	for _, tag := range tagger.Tag(src) {
		switch {
		case strings.HasPrefix(tag.Kind, "definition"):
			qn := rel + ":" + tag.Name
			def := tsDef{
				name:      tag.Name,
				qname:     qn,
				kind:      tsKind(tag.Kind),
				startByte: tag.Range.StartByte,
				endByte:   tag.Range.EndByte,
				line:      int(tag.NameRange.StartPoint.Row) + 1,
				col:       int(tag.NameRange.StartPoint.Column) + 1,
				signature: firstLine(src, tag.Range.StartByte, tag.Range.EndByte),
			}
			tf.defs = append(tf.defs, def)
			nameToQNames[tag.Name] = append(nameToQNames[tag.Name], qn)
			_ = sink.Symbol(Symbol{
				QName: qn, Name: def.name, Kind: def.kind, File: rel,
				Signature: def.signature,
				StartLine: def.line, StartCol: def.col,
				EndLine: int(tag.Range.EndPoint.Row) + 1, EndCol: int(tag.Range.EndPoint.Column) + 1,
			})
		case strings.HasPrefix(tag.Kind, "reference"):
			tf.refs = append(tf.refs, tsRef{
				name:      tag.Name,
				startByte: tag.NameRange.StartByte,
				endByte:   tag.NameRange.EndByte,
				line:      int(tag.NameRange.StartPoint.Row) + 1,
				col:       int(tag.NameRange.StartPoint.Column) + 1,
			})
		}
	}
	return tf, true
}

// emitTSEdges resolves each heuristic call site to a definition and emits the
// reference plus a CALLS edge from its enclosing definition.
func emitTSEdges(files []tsFile, nameToQNames map[string][]string, sink Sink) {
	for _, f := range files {
		for _, ref := range f.refs {
			caller := enclosingDef(f.defs, ref.startByte, ref.endByte)
			target, conf := resolveCallee(ref.name, f, nameToQNames)
			if target == "" {
				continue
			}
			_ = sink.Reference(Reference{ToQName: target, File: f.rel, Line: ref.line, Col: ref.col, Role: "call"})
			if caller != "" && caller != target {
				_ = sink.Edge(Edge{FromQName: caller, ToQName: target, Kind: "CALLS", Confidence: conf})
			}
		}
	}
}

// tsKind normalizes a tree-sitter tag kind ("definition.function") to codenav's
// flat kind vocabulary.
func tsKind(tagKind string) string {
	k := strings.TrimPrefix(tagKind, "definition.")
	switch k {
	case "function":
		return "func"
	case "method":
		return "method"
	case "class", "interface", "struct", "enum", "type", "trait":
		return "type"
	case "constant":
		return "const"
	case "module":
		return "module"
	default:
		return k
	}
}

// firstLine returns the trimmed first line of a byte span, as a pseudo-signature.
func firstLine(src []byte, start, end uint32) string {
	if int(start) >= len(src) {
		return ""
	}
	if int(end) > len(src) {
		end = uint32(len(src)) // #nosec G115 -- len(src) is bounded by maxFileBytes (1 MiB)
	}
	seg := src[start:end]
	if nl := strings.IndexByte(string(seg), '\n'); nl >= 0 {
		seg = seg[:nl]
	}
	return strings.TrimSpace(string(seg))
}
