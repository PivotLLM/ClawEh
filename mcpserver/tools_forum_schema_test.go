// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/PivotLLM/ClawEh/forum"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

// forumDefs serves the forum's real tool definitions; building them does
// not touch the service or host.
type forumDefs struct{}

func (forumDefs) RegisterTools(global.Deps) []global.ToolDefinition { return forum.Tools(nil, nil) }

// tools/list publishes forum_config_import's config and
// forum_config_update's changes with the configuration's full schema.
func TestToolsList_PublishesForumConfigSchema(t *testing.T) {
	built := tools.NamespacedProvider("forum", forumDefs{}).Build(tools.ToolDeps{})
	srv, err := New(
		WithAgentRegistries(map[string]*tools.ToolRegistry{"alice": newRegistryWith(built...)}),
		WithAllowlist([]string{"*"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(srv.srv.HandleMessage(context.Background(), body))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]map[string]any `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		arg    string
		schema map[string]any
	}{
		"forum_config_import": {"config", forum.ConfigSchema()},
		"forum_config_update": {"changes", forum.PatchSchema()},
	}
	found := 0
	for _, tl := range parsed.Result.Tools {
		w, ok := want[tl.Name]
		if !ok {
			continue
		}
		found++
		// Compare through JSON, as published.
		var wantProps map[string]any
		data, err := json.Marshal(w.schema["properties"])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &wantProps); err != nil {
			t.Fatal(err)
		}
		if got := tl.InputSchema.Properties[w.arg]["properties"]; !reflect.DeepEqual(got, wantProps) {
			t.Errorf("%s.%s published %v, want the configuration schema", tl.Name, w.arg, got)
		}
	}
	if found != 2 {
		t.Errorf("tools/list had %d of the two config tools: %s", found, raw)
	}
}
