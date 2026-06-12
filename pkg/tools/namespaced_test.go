package tools

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

// fakeGlobalProvider returns bare-named tools for bridge testing.
type fakeGlobalProvider struct{}

func (fakeGlobalProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	return []global.ToolDefinition{
		{
			Name:        "read",
			Description: "read a thing",
			Parameters: []global.Parameter{
				{Name: "path", Type: "string", Required: true, Description: "the path"},
			},
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return &global.Result{ForLLM: "read:" + call.Args["path"].(string) + " sess=" + call.Session}, nil
			},
		},
		{
			Name:          "watch",
			Description:   "watch a thing",
			SessionScoped: true,
			Async:         true,
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if call.Notify != nil {
					call.Notify(&global.Result{ForLLM: "done"})
				}
				return &global.Result{ForLLM: "started", Async: true}, nil
			},
		},
	}
}

func TestNamespacedProvider_PrefixesNames(t *testing.T) {
	p := NamespacedProvider("file", fakeGlobalProvider{})
	built := p.Build(ToolDeps{})
	got := map[string]bool{}
	for _, tl := range built {
		got[tl.Name()] = true
	}
	if !got["file_read"] || !got["file_watch"] {
		t.Fatalf("expected file_read and file_watch, got %v", got)
	}
}

func TestNamespacedProvider_ExecuteAndSchema(t *testing.T) {
	p := NamespacedProvider("file", fakeGlobalProvider{})
	var readTool Tool
	for _, tl := range p.Build(ToolDeps{}) {
		if tl.Name() == "file_read" {
			readTool = tl
		}
	}
	if readTool == nil {
		t.Fatal("file_read not built")
	}
	// Schema generated from []Parameter.
	schema := readTool.Parameters()
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["path"]; !ok {
		t.Fatalf("expected 'path' property in schema, got %v", schema)
	}
	// Session is threaded from ctx into the call.
	ctx := WithSessionKey(context.Background(), "sess-1")
	res := readTool.Execute(ctx, map[string]any{"path": "/x"})
	if res.IsError || res.ForLLM != "read:/x sess=sess-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestNamespacedProvider_AsyncAndSessionInterfaces(t *testing.T) {
	p := NamespacedProvider("file", fakeGlobalProvider{})
	var watch Tool
	for _, tl := range p.Build(ToolDeps{}) {
		if tl.Name() == "file_watch" {
			watch = tl
		}
	}
	if watch == nil {
		t.Fatal("file_watch not built")
	}
	// def flags must surface as the optional interfaces.
	ss, ok := watch.(SessionScoped)
	if !ok || !ss.IsSessionScoped() {
		t.Fatal("file_watch should be SessionScoped")
	}
	ae, ok := watch.(AsyncExecutor)
	if !ok {
		t.Fatal("file_watch should be AsyncExecutor")
	}
	var got *ToolResult
	res := ae.ExecuteAsync(context.Background(), map[string]any{}, func(_ context.Context, r *ToolResult) { got = r })
	if !res.Async {
		t.Fatalf("expected async result, got %+v", res)
	}
	if got == nil || got.ForLLM != "done" {
		t.Fatalf("expected notify callback to deliver 'done', got %+v", got)
	}
	// The non-async, non-session tool must NOT satisfy those interfaces.
	read := wrapGlobalTool("file", fakeGlobalProvider{}.RegisterTools(global.Deps{})[0])
	if _, ok := read.(AsyncExecutor); ok {
		t.Fatal("file_read must not be AsyncExecutor")
	}
	if _, ok := read.(SessionScoped); ok {
		t.Fatal("file_read must not be SessionScoped")
	}
}

func TestNamespacedProvider_Describe(t *testing.T) {
	p := NamespacedProvider("file", fakeGlobalProvider{})
	descs := p.Describe()
	var readDesc *ToolDescriptor
	for i := range descs {
		if descs[i].Name == "file_read" {
			readDesc = &descs[i]
		}
	}
	if readDesc == nil || !readDesc.DefaultEnabled {
		t.Fatalf("expected file_read descriptor with DefaultEnabled, got %+v", descs)
	}
}
