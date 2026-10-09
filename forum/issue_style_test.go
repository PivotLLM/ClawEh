package forum

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// TestIssueMessageStyle holds every validation issue message written in the
// package to the rule on Issue: it continues "<path>: ", so it starts in
// lower case and has no closing full stop. It checks the literal message of
// each addf call and Issue{Message: ...} (directly or as fmt.Sprintf's
// format); a message that starts with a verb such as %s or %q (a name or
// ID) passes.
func TestIssueMessageStyle(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	check := func(e ast.Expr) {
		if call, ok := e.(*ast.CallExpr); ok && isSelector(call.Fun, "fmt", "Sprintf") && len(call.Args) > 0 {
			e = call.Args[0]
		}
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		msg, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("%s: %v", fset.Position(lit.Pos()), err)
		}
		checked++
		r, _ := utf8.DecodeRuneInString(msg)
		if unicode.IsUpper(r) || strings.HasSuffix(msg, ".") {
			t.Errorf("%s: issue message %q must start in lower case and have no closing full stop", fset.Position(lit.Pos()), msg)
		}
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "addf" && len(n.Args) > 1 {
					check(n.Args[1])
				}
			case *ast.CompositeLit:
				if id, ok := n.Type.(*ast.Ident); ok && id.Name == "Issue" {
					for _, el := range n.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok && isIdent(kv.Key, "Message") {
							check(kv.Value)
						}
					}
				}
			}
			return true
		})
	}
	if checked < 50 {
		t.Fatalf("checked only %d issue messages; the scan no longer finds them", checked)
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name && isIdent(sel.X, pkg)
}
