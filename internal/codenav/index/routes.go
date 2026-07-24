package index

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// routes.go detects web-framework route registrations in Go source and emits
// route nodes linked to their handler symbol — the "follow web routes" feature.
// Detection is syntactic (method name + string path literal), covering the
// common registration shapes for net/http, chi, gin and echo.

// detectRoutes scans a package's syntax for route registrations, attributing
// each to the repo-relative file where the registration call appears so an
// incremental refresh can drop and recompute just that file's routes.
func detectRoutes(root string, pkg *packages.Package, sink Sink) {
	if pkg.TypesInfo == nil {
		return
	}
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			method, isRoute := routeMethod(sel.Sel.Name)
			if !isRoute {
				return true
			}
			// Require a path-like first argument to suppress false positives
			// (e.g. cache.Get("key", v) is not a route).
			path, ok := stringLit(call.Args[0])
			if !ok || !strings.HasPrefix(path, "/") {
				return true
			}
			rel, _ := relWithin(root, pkg.Fset.Position(call.Pos()).Filename)
			_ = sink.Route(Route{
				Method:       method,
				Pattern:      path,
				HandlerQName: handlerQName(pkg, call.Args[len(call.Args)-1]),
				Framework:    frameworkOf(sel.Sel.Name),
				File:         rel,
			})
			return true
		})
	}
}

// routeMethod maps a registration method name to an HTTP method.
func routeMethod(name string) (string, bool) {
	switch name {
	case "HandleFunc", "Handle":
		return "ANY", true // net/http, gorilla/mux, chi
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "CONNECT", "TRACE":
		return name, true // gin, echo
	case "Get", "Post", "Put", "Delete", "Patch", "Head", "Options":
		return strings.ToUpper(name), true // chi
	}
	return "", false
}

func frameworkOf(method string) string {
	switch method {
	case "HandleFunc", "Handle":
		return "net/http"
	}
	if method == strings.ToUpper(method) {
		return "gin/echo"
	}
	return "chi"
}

// handlerQName resolves a handler argument expression to a symbol qname.
func handlerQName(pkg *packages.Package, e ast.Expr) string {
	switch h := e.(type) {
	case *ast.Ident:
		if obj := pkg.TypesInfo.Uses[h]; obj != nil {
			return objQName(obj)
		}
		if obj := pkg.TypesInfo.Defs[h]; obj != nil {
			return objQName(obj)
		}
	case *ast.SelectorExpr:
		if obj := pkg.TypesInfo.Uses[h.Sel]; obj != nil {
			return objQName(obj)
		}
	}
	return "" // func literals and unresolved handlers leave the link empty
}

func stringLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return s, true
}
