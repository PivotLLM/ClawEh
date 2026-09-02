package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// restartServices is the reload path. It rebuilds every service from the new
// config, and it is not unit-testable end to end without standing up an agent
// loop, a channel manager, cron, media and MCP — so these guards assert on the
// source instead. Each one exists because dropping the call is silent: the
// gateway keeps running and the loss only shows up in production.
func restartServicesBody(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "helpers.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing helpers.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "restartServices" || fn.Body == nil {
			continue
		}
		var sb strings.Builder
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				sb.WriteString(dottedName(call.Fun) + "\n")
			}
			return true
		})
		return sb.String()
	}
	t.Fatal("restartServices not found in helpers.go — this guard needs updating")
	return ""
}

// dottedName renders a call target as source text ("services.MountWatcher.Start"),
// so a guard can name the whole selector chain rather than just its last segment.
func dottedName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if prefix := dottedName(v.X); prefix != "" {
			return prefix + "." + v.Sel.Name
		}
		return v.Sel.Name
	default:
		return ""
	}
}

// TestReloadReAppliesTheAllowlist is the regression guard for `claw network`
// against a running gateway. The listener is deliberately never recreated on
// reload (that is what keeps WebUI WebSockets alive), so the allowlist only
// changes if the reload path pushes it. Drop the call and the command reports
// success while the gateway keeps enforcing the old allowlist — an operator
// locked out stays locked out, with no error anywhere.
func TestReloadReAppliesTheAllowlist(t *testing.T) {
	body := restartServicesBody(t)
	if !strings.Contains(body, "services.HTTPHost.SetAllowlist") {
		t.Fatal("restartServices no longer calls HTTPHost.SetAllowlist; a config change to gateway.allowed_cidrs would need a restart again")
	}
}

// TestReloadRebuildsTheServicesItStops keeps the two halves of a reload in step:
// anything stopAndCleanupServices tears down has to be rebuilt here, which is
// exactly the invariant the mount watcher broke.
func TestReloadRebuildsTheServicesItStops(t *testing.T) {
	body := restartServicesBody(t)
	for _, want := range []string{
		"channels.NewManager", // channel manager
		"devices.NewService",  // device service
		"setupCronTool",       // cron
	} {
		if !strings.Contains(body, want) {
			t.Errorf("restartServices does not call %s; stopAndCleanupServices stops that service on every reload", want)
		}
	}
}
