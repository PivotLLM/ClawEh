package gateway

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/channels/device"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
	"github.com/PivotLLM/ClawEh/providers"
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
		"mountwatch.New",        // mount watcher
		"ChannelManager.Reload", // channels (kept running; reloaded in place)
		"devices.NewService",    // device service
		"setupCronTool",         // cron
	} {
		if !strings.Contains(body, want) {
			t.Errorf("restartServices does not call %s; stopAndCleanupServices stops that service on every reload", want)
		}
	}
}

// A device channel rebuilt by a reload gets what the gateway's build hook
// provides, as the first one did: the agent querier (agents.list and
// chat.history for operator clients), the HTTPS certificate, without which
// a channel with channels.device.tls on refuses to start, and the admin
// account's username as the default device user.
func TestChannelBuildHook_AppliedToRebuiltDeviceChannel(t *testing.T) {
	t.Setenv(global.EnvVarHome, t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents.BaseDir = t.TempDir()
	cfg.Agents.Defaults.Models = []string{"test-model"}
	cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %T", ln.Addr())
	}
	port := addr.Port
	if err = ln.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Channels.Device = config.DeviceChannelConfig{
		Enabled: true, Token: "tok", Host: "127.0.0.1", Port: port, TLS: true,
		AllowFrom: config.FlexibleStringSlice{"*"},
	}

	mb := bus.NewMessageBus()
	defer mb.Close()
	al, err := agent.NewAgentLoop(cfg, mb, providers.NewUnconfiguredProvider(), nil)
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	certs, err := tlscert.Load(tlscert.OptionsFromConfig(cfg))
	if err != nil {
		t.Fatalf("tlscert.Load: %v", err)
	}
	credsPath := filepath.Join(t.TempDir(), admin.FileName)
	if err = admin.Write(credsPath, "bob", "a long enough password"); err != nil {
		t.Fatalf("admin.Write: %v", err)
	}
	m, err := channels.NewManager(cfg, mb, nil, channels.WithBuildHook(channelBuildHook(al, certs, credsPath)))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	first, _ := m.GetChannel("device")

	changed := *cfg
	changed.Gateway.TrustedProxies = []string{"192.0.2.2"}
	if res := m.Reload(context.Background(), &changed, nil); !slices.Equal(res.Restarted, []string{"device"}) {
		t.Fatalf("reload = %+v, want device restarted", res)
	}
	ch, _ := m.GetChannel("device")
	rebuilt, ok := ch.(*device.DeviceChannel)
	if !ok || ch == first {
		t.Fatalf("device channel not rebuilt: %T", ch)
	}
	if !rebuilt.IsRunning() {
		t.Fatal("the rebuilt device channel did not start: it was not given the HTTPS certificate")
	}
	q, ok := rebuilt.AgentQuerier().(deviceAgentQuerier)
	if !ok || q.al != al {
		t.Fatalf("the rebuilt device channel's agent querier = %T, want the agent loop's", rebuilt.AgentQuerier())
	}
	if got := rebuilt.DefaultUser(); got != "bob" {
		t.Fatalf("the rebuilt device channel's default user = %q, want the admin account's %q", got, "bob")
	}
	// The default user follows a changed admin account without a rebuild.
	if werr := admin.Write(credsPath, "alice", "a long enough password"); werr != nil {
		t.Fatalf("admin.Write: %v", werr)
	}
	if got := rebuilt.DefaultUser(); got != "alice" {
		t.Fatalf("default user after the admin account changed = %q, want %q", got, "alice")
	}
}
