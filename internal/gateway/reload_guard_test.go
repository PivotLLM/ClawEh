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
// gateway keeps running and the loss only shows up in production. The calls
// of restartServices include those of the services.go functions it calls,
// so a step moved into a helper still counts.
func restartServicesBody(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "services.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing services.go: %v", err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
			funcs[fn.Name.Name] = fn
		}
	}
	if funcs["restartServices"] == nil {
		t.Fatal("restartServices not found in services.go — this guard needs updating")
	}
	var sb strings.Builder
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		ast.Inspect(funcs[name].Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				callee := dottedName(call.Fun)
				sb.WriteString(callee + "\n")
				if funcs[callee] != nil {
					walk(callee)
				}
			}
			return true
		})
	}
	walk("restartServices")
	return sb.String()
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

// TestReloadRebuildsTheMountWatcher guards both halves of the panic fixed
// alongside it: stopAndCleanupServices stops the watcher on every reload, so if
// restartServices does not build a new one, mount notifications die silently
// after the first reload — and the old code then closed an already-closed
// channel on the second, taking the process down.
func TestReloadRebuildsTheMountWatcher(t *testing.T) {
	body := restartServicesBody(t)
	if !strings.Contains(body, "mountwatch.New") {
		t.Fatal("restartServices no longer rebuilds services.MountWatcher; mount notifications stop after the first config reload")
	}
	if !strings.Contains(body, "services.MountWatcher.Start") {
		t.Fatal("restartServices builds a mount watcher but never starts it")
	}
}

// TestReloadRebuildsTheServicesItStops keeps the two halves of a reload in step:
// anything stopAndCleanupServices tears down has to be rebuilt here, which is
// exactly the invariant the mount watcher broke.
func TestReloadRebuildsTheServicesItStops(t *testing.T) {
	body := restartServicesBody(t)
	for _, want := range []string{
		"mountwatch.New",      // mount watcher
		"channels.NewManager", // channel manager
		"devices.NewService",  // device service
		"setupCronTool",       // cron
	} {
		if !strings.Contains(body, want) {
			t.Errorf("restartServices does not call %s; stopAndCleanupServices stops that service on every reload", want)
		}
	}
}
