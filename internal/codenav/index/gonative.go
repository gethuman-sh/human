package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// GoNative is the precise Go backend. It uses go/packages for type-resolved
// symbols and references, and a CHA call graph for precise CALLS edges.
//
// Qualified names use the import path so they are globally meaningful and line
// up across repositories (enabling Go cross-repo linking in M2):
//
//	<import-path>.<Name>            for funcs, types, vars, consts
//	<import-path>.<Recv>.<Method>   for methods
type GoNative struct{}

func (GoNative) Name() string       { return "go" }
func (GoNative) Fidelity() Fidelity { return Precise }

func (GoNative) CanHandle(scan RepoScan) bool {
	_, err := os.Stat(filepath.Join(scan.Root, "go.mod"))
	return err == nil
}

func (g GoNative) Index(ctx context.Context, scan RepoScan, sink Sink) error {
	// Whole-module load; a repo with no Go packages is a real "nothing to index".
	return g.indexPatterns(ctx, scan, []string{"./..."}, map[string]bool{}, true, sink)
}

// IndexIncremental reloads only the Go packages that own a changed .go file,
// seeding `defined` with every existing repo qname so a reprocessed package's
// references/edges into unchanged packages still resolve. It implements
// IncrementalIndexer.
func (g GoNative) IndexIncremental(ctx context.Context, scan RepoScan, delta Delta, prior PriorIndex, sink Sink) error {
	dirs := goPackageDirs(delta)
	if len(dirs) == 0 {
		return nil // no Go change
	}
	defined, err := prior.DefinedQNames()
	if err != nil {
		return err
	}
	return g.indexPatterns(ctx, scan, dirs, defined, false, sink)
}

// indexPatterns loads the given package patterns and runs the four extraction
// passes against `defined` (pre-seeded in the incremental path). When full is
// false it tolerates patterns that match no loadable package (a deleted or
// emptied dir) instead of treating that as fatal — cleanup of such a package's
// stale rows is left to the writer's RemoveFiles.
func (g GoNative) indexPatterns(ctx context.Context, scan RepoScan, patterns []string, defined map[string]bool, full bool, sink Sink) error {
	cfg := &packages.Config{
		Mode:    packages.LoadAllSyntax | packages.NeedModule,
		Dir:     scan.Root,
		Context: ctx,
		Tests:   false,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		if full || !isNoPackagesErr(err) {
			return fmt.Errorf("load packages: %w", err)
		}
		// Incremental: a pattern for a removed dir may surface as a top-level
		// "matched no packages" error; continue with whatever loaded.
	}
	if full && len(pkgs) == 0 {
		return fmt.Errorf("no Go packages found under %s", scan.Root)
	}
	// In the incremental path, drop packages that carry load errors and hold no
	// Go files (a deleted/emptied dir): emit nothing so RemoveFiles clears them.
	if !full {
		pkgs = loadablePkgs(pkgs)
	}

	// Pass A: symbols + files. Pass B: references to repo-local symbols.
	emitSymbolsAndFiles(scan, pkgs, sink, defined)
	emitReferences(scan, pkgs, defined, sink)

	// Pass C: framework route detection (route nodes + handler links).
	for _, pkg := range pkgs {
		detectRoutes(scan.Root, pkg, sink)
	}

	// Pass D: precise CALLS edges via CHA call graph. The CHA builder in
	// golang.org/x/tools can panic on packages that use generics (it reaches
	// types.TypeParam during RuntimeTypes traversal). Recover so the repo still
	// gets symbols, references, routes, and search — only the call graph
	// (callers/callees/callpath/impact) is skipped when this fires.
	buildCallGraph(scan, pkgs, defined, sink)
	return nil
}

// goPackageDirs maps a delta's changed .go files to per-directory load patterns
// ("./sub/pkg"), normalizing the repo root ("." from filepath.Dir) to "./" and
// deduping. It returns nil when no .go file changed.
func goPackageDirs(delta Delta) []string {
	seen := map[string]bool{}
	var out []string
	add := func(rel string) {
		if !strings.HasSuffix(rel, ".go") {
			return
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		pattern := "./" + dir
		if dir == "." {
			pattern = "./"
		}
		if !seen[pattern] {
			seen[pattern] = true
			out = append(out, pattern)
		}
	}
	for _, p := range delta.Added {
		add(p)
	}
	for _, p := range delta.Modified {
		add(p)
	}
	for _, p := range delta.Deleted {
		add(p)
	}
	return out
}

// loadablePkgs keeps only packages that actually contributed Go files; a package
// returned solely to carry a load error (no GoFiles) is dropped so its stale
// rows are cleaned up by RemoveFiles rather than re-emitted empty.
func loadablePkgs(pkgs []*packages.Package) []*packages.Package {
	out := pkgs[:0]
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 && len(pkg.GoFiles) == 0 {
			continue
		}
		out = append(out, pkg)
	}
	return out
}

// isNoPackagesErr reports whether a packages.Load error is the benign
// "matched no packages" case a deleted directory pattern produces.
func isNoPackagesErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "matched no packages") || strings.Contains(msg, "no packages")
}

// emitSymbolsAndFiles records each repo-local file once and emits a Symbol for
// every top-level declaration, tracking which qnames are defined in the repo.
func emitSymbolsAndFiles(scan RepoScan, pkgs []*packages.Package, sink Sink, defined map[string]bool) {
	seenFile := map[string]bool{}
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			filename := pkg.Fset.Position(file.Pos()).Filename
			rel, ok := relWithin(scan.Root, filename)
			if !ok {
				continue // dependency file outside the repo
			}
			if !seenFile[rel] {
				seenFile[rel] = true
				_ = sink.File(FileRec{Path: rel, Lang: "go", ContentHash: hashFile(filename), Fidelity: Precise})
			}
			for _, decl := range file.Decls {
				collectDecl(pkg, scan.Root, decl, sink, defined)
			}
		}
	}
}

// emitReferences records each use-site of a symbol defined inside the repo.
func emitReferences(scan RepoScan, pkgs []*packages.Package, defined map[string]bool, sink Sink) {
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for ident, obj := range pkg.TypesInfo.Uses {
			qn := objQName(obj)
			if qn == "" || !defined[qn] {
				continue
			}
			pos := pkg.Fset.Position(ident.Pos())
			rel, ok := relWithin(scan.Root, pos.Filename)
			if !ok {
				continue
			}
			_ = sink.Reference(Reference{ToQName: qn, File: rel, Line: pos.Line, Col: pos.Column, Role: "ref"})
		}
	}
}

// buildCallGraph emits CALLS edges from a CHA call graph, recovering from the
// x/tools generics panic so a single unsupported construct doesn't abort the
// whole index.
func buildCallGraph(scan RepoScan, pkgs []*packages.Package, defined map[string]bool, sink Sink) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr,
				"codenav: call graph skipped for %q (go/x-tools limitation: %v); definitions and references are still indexed\n",
				scan.Project, r)
		}
	}()

	prog, _ := ssautil.AllPackages(pkgs, 0)
	prog.Build()
	cg := cha.CallGraph(prog)
	for fn, node := range cg.Nodes {
		if fn == nil {
			continue
		}
		// Roll calls made inside closures/defers up to the nearest named
		// enclosing function so they are not lost (anonymous funcs have no qname).
		from := enclosingQName(fn)
		if from == "" || !defined[from] {
			continue
		}
		for _, e := range node.Out {
			if e.Callee == nil || e.Callee.Func == nil {
				continue
			}
			obj := e.Callee.Func.Object()
			if obj == nil {
				continue
			}
			to := objQName(obj)
			if to == "" {
				continue
			}
			switch {
			case defined[to]:
				_ = sink.Edge(Edge{FromQName: from, ToQName: to, Kind: "CALLS", Confidence: 1.0})
			case obj.Pkg() != nil && isExternalModule(obj.Pkg().Path()):
				// Candidate for cross-repo linking: the store relabels it
				// CROSS_CALLS if the target is defined in another indexed repo,
				// and drops it otherwise (stdlib / unindexed deps).
				_ = sink.Edge(Edge{FromQName: from, ToQName: to, Kind: "CALLS", Confidence: 1.0})
			}
		}
	}
}

// collectDecl emits a Symbol for each top-level declaration.
func collectDecl(pkg *packages.Package, root string, decl ast.Decl, sink Sink, defined map[string]bool) {
	emit := func(name *ast.Ident, node ast.Node, kind string) {
		obj := pkg.TypesInfo.Defs[name]
		if obj == nil {
			return
		}
		qn := objQName(obj)
		if qn == "" {
			return
		}
		start := pkg.Fset.Position(node.Pos())
		end := pkg.Fset.Position(node.End())
		rel, ok := relWithin(root, start.Filename)
		if !ok {
			return
		}
		defined[qn] = true
		_ = sink.Symbol(Symbol{
			QName:     qn,
			Name:      obj.Name(),
			Kind:      kind,
			File:      rel,
			Signature: signatureOf(obj),
			StartLine: start.Line, StartCol: start.Column,
			EndLine: end.Line, EndCol: end.Column,
		})
	}

	switch d := decl.(type) {
	case *ast.FuncDecl:
		kind := "func"
		if d.Recv != nil {
			kind = "method"
		}
		emit(d.Name, d, kind)
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				emit(s.Name, s, "type")
			case *ast.ValueSpec:
				kind := "var"
				if d.Tok == token.CONST {
					kind = "const"
				}
				for _, n := range s.Names {
					emit(n, s, kind)
				}
			}
		}
	}
}

// enclosingQName returns the qname of fn, or of its nearest named ancestor if
// fn is an anonymous function (closure). This attributes calls made inside
// closures, defers and goroutine bodies to the named function that contains them.
func enclosingQName(fn *ssa.Function) string {
	for fn != nil {
		if qn := objQName(fn.Object()); qn != "" {
			return qn
		}
		fn = fn.Parent()
	}
	return ""
}

// objQName computes a stable, import-path-qualified name for an object.
func objQName(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	pkgPath := obj.Pkg().Path()
	if fn, ok := obj.(*types.Func); ok {
		if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
			return pkgPath + "." + recvTypeName(sig.Recv().Type()) + "." + fn.Name()
		}
	}
	return pkgPath + "." + obj.Name()
}

// isExternalModule reports whether an import path belongs to an external module
// (its first path segment is a domain, e.g. "github.com") rather than the
// standard library ("fmt", "net/http"). Only such targets are cross-repo
// candidates, which keeps stdlib calls out of the buffered edge set.
func isExternalModule(pkgPath string) bool {
	seg := pkgPath
	if before, _, ok := strings.Cut(pkgPath, "/"); ok {
		seg = before
	}
	return strings.Contains(seg, ".")
}

func recvTypeName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return t.String()
}

// signatureOf renders a compact, package-name-qualified signature.
func signatureOf(obj types.Object) string {
	qual := func(p *types.Package) string { return p.Name() }
	switch o := obj.(type) {
	case *types.Func:
		// "func(args) results" -> prepend name for readability.
		return "func " + o.Name() + strings.TrimPrefix(types.TypeString(o.Type(), qual), "func")
	default:
		return types.TypeString(obj.Type(), qual)
	}
}

func relWithin(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func hashFile(path string) string {
	b, err := os.ReadFile(path) // #nosec G304 -- hashes a repo source file by path
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
