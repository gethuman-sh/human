package marker_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Marker comments are the pipeline's wire protocol, and the one rule that keeps
// them evolvable is that only internal/marker knows their layout. Everyone else
// calls ParseBody.
//
// This test exists because that rule was convention rather than enforcement, and
// convention lost: marker.Sign began splicing `machine:`/`build:` in as the
// first lines after the header, while board_state.go still read "the first line
// after the header" positionally. Every failed card on the board reported its
// signature instead of its diagnosis, and the test suite could not catch it
// because the tests built unsigned markers — encoding the same stale assumption
// as the code.
//
// So the guard is deliberately not "the production code is correct". It is
// "nobody outside this package may read a marker body by position", and it
// covers _test.go files too, because that is where the assumption hid.
func TestNoPositionalMarkerParsingOutsideThisPackage(t *testing.T) {
	root := repoRoot(t)
	var violations []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// internal/marker owns the layout; it is the one place allowed to know it.
		if strings.Contains(filepath.ToSlash(path), "internal/marker/") {
			return nil
		}
		violations = append(violations, positionalReadsIn(t, path, root)...)
		return nil
	})
	require.NoError(t, err)

	require.Empty(t, violations,
		"a marker body must be read with marker.ParseBody, never by line position — "+
			"the field block (machine:/build: and any future signature) sits between the "+
			"header and the prose, so 'the line after the header' is not the diagnosis:\n  %s",
		strings.Join(violations, "\n  "))
}

// positionalReadsIn reports the two idioms that read a marker body by position:
// trimming a header constant off the front, and cutting the body at its first
// newline. Both were how the signing regression was written.
func positionalReadsIn(t *testing.T, path, root string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// A file that does not parse is not this test's problem; the build says so.
		return nil
	}
	rel, _ := filepath.Rel(root, path)

	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, isIdent := fn.X.(*ast.Ident)
		if !isIdent || pkg.Name != "strings" || len(call.Args) < 2 {
			return true
		}
		pos := fset.Position(call.Pos())
		switch fn.Sel.Name {
		case "TrimPrefix":
			// strings.TrimPrefix(body, SomethingHeader) — slicing the header off to
			// read what follows by position.
			if mentionsMarkerHeader(call.Args[1]) {
				found = append(found, rel+":"+itoa(pos.Line)+" strings.TrimPrefix off a marker header")
			}
		case "Cut", "SplitN", "Split":
			// strings.Cut(body, "\n") — "the first line, then the rest".
			if isNewlineLiteral(call.Args[1]) && mentionsMarkerHeader(call.Args[0]) {
				found = append(found, rel+":"+itoa(pos.Line)+" strings."+fn.Sel.Name+" on a marker body by newline")
			}
		}
		return true
	})
	return found
}

// mentionsMarkerHeader reports whether an expression involves a marker header
// constant — an identifier ending in "Header", possibly inside a concatenation
// such as `DeployFailedHeader + "\n"`.
func mentionsMarkerHeader(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return strings.HasSuffix(v.Name, "Header")
	case *ast.SelectorExpr:
		return strings.HasSuffix(v.Sel.Name, "Header")
	case *ast.BinaryExpr:
		return mentionsMarkerHeader(v.X) || mentionsMarkerHeader(v.Y)
	case *ast.CallExpr:
		for _, a := range v.Args {
			if mentionsMarkerHeader(a) {
				return true
			}
		}
	}
	return false
}

func isNewlineLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && (lit.Value == `"\n"` || lit.Value == "`\n`")
}

func skipDir(name string) bool {
	switch name {
	// .claude holds throwaway agent worktrees — copies of this repo whose
	// stale code is not this checkout's to answer for.
	case ".git", ".claude", "node_modules", "vendor", "dist", "testdata":
		return true
	}
	return false
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "could not find the module root")
		dir = parent
	}
}
