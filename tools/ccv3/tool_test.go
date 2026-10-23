// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ccv3pkg "github.com/PivotLLM/ClawEh/ccv3"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

// testConfig is a configuration under a temporary CLAW_HOME: alice has the
// switch, bob does not, Carol (named) has the switch and the card
// "Sherlock", Dave the card "sherlock" too.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv(global.EnvVarHome, t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alice", CCv3: true},
		{ID: "bob"},
		{ID: "carol", Name: "Carol", CCv3: true, CharacterCard: "Sherlock"},
	}
	return cfg
}

func build(t *testing.T, cfg *config.Config, agentID string) []tools.Tool {
	t.Helper()
	return tools.BareNamespacedProvider(Suite, GlobalProvider).Build(tools.ToolDeps{Cfg: cfg, AgentID: agentID})
}

// run calls the one ccv3 tool of alice.
func run(t *testing.T, cfg *config.Config, args map[string]any) *tools.ToolResult {
	t.Helper()
	ts := build(t, cfg, "alice")
	if len(ts) != 1 {
		t.Fatalf("alice has %d tools, want 1", len(ts))
	}
	return ts[0].Execute(context.Background(), args)
}

func mustOK(t *testing.T, res *tools.ToolResult) {
	t.Helper()
	if res.IsError {
		t.Fatalf("result is an error: %s (%v)", res.ForLLM, res.Err)
	}
}

// mustRefuse checks res is an expected refusal whose text contains want.
func mustRefuse(t *testing.T, res *tools.ToolResult, want string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("result is not an error: %s", res.ForLLM)
	}
	if !tools.IsExpectedRefusal(res.Err) {
		t.Errorf("refusal %q: Err %v is not an expected refusal", res.ForLLM, res.Err)
	}
	if !strings.Contains(res.ForLLM, want) {
		t.Errorf("refusal %q does not contain %q", res.ForLLM, want)
	}
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%v is not an object", v)
	}
	return m
}

func card(name string) map[string]any {
	return map[string]any{"name": name, "description": "A detective.", "mes_example": "<START>\n{{char}}: Hm.", "tags": []any{"detective"}}
}

func TestGating(t *testing.T) {
	cfg := testConfig(t)
	if ts := build(t, cfg, "bob"); len(ts) != 0 {
		t.Errorf("bob (switch off) has %d tools", len(ts))
	}
	if ts := build(t, cfg, "nobody"); len(ts) != 0 {
		t.Errorf("an unknown agent has %d tools", len(ts))
	}
	ts := build(t, cfg, "Alice")
	if len(ts) != 1 || ts[0].Name() != "ccv3" {
		t.Fatalf("alice tools = %v, want one named ccv3", ts)
	}
	params := ts[0].Parameters()
	props := asMap(t, params["properties"])
	for _, name := range []string{"action", "id", "new_id", "card", "changes"} {
		if _, ok := props[name]; !ok {
			t.Errorf("schema has no %s", name)
		}
	}
	if asMap(t, props["card"])["additionalProperties"] != false {
		t.Error("card argument schema is not the closed card schema")
	}
	// The enumeration pass registers nothing.
	if defs := GlobalProvider.RegisterTools(global.Deps{}); len(defs) != 0 {
		t.Errorf("enumeration pass: %d tools", len(defs))
	}
}

// The description is the guide: short, naming every action and field.
func TestDescription(t *testing.T) {
	if n := len(description); n > 1500 {
		t.Errorf("description is %d characters", n)
	}
	for _, a := range actions {
		if !strings.Contains(description, a) {
			t.Errorf("description does not name action %s", a)
		}
	}
	props := asMap(t, ccv3pkg.CardSchema()["properties"])
	for name := range props {
		if !strings.Contains(description, name) {
			t.Errorf("description does not name field %s", name)
		}
	}
	for _, s := range []string{"{{char}}", "{{user}}", "{{//", "strict", "WebUI", "agent_spawn", "forum"} {
		if !strings.Contains(description, s) {
			t.Errorf("description does not mention %s", s)
		}
	}
}

func TestActions(t *testing.T) {
	cfg := testConfig(t)

	res := run(t, cfg, map[string]any{"action": "create", "id": "Sherlock", "card": card("Sherlock Holmes")})
	mustOK(t, res)
	var created ccv3pkg.Card
	if err := json.Unmarshal([]byte(res.ForLLM), &created); err != nil || created.Spec != ccv3pkg.SpecV3 {
		t.Fatalf("create result %s: %v", res.ForLLM, err)
	}
	var data struct {
		MesExample string `json:"mes_example"`
	}
	if err := json.Unmarshal(created.Data, &data); err != nil || data.MesExample != "<START>\n{{char}}: Hm." {
		t.Errorf("mes_example = %q, %v", data.MesExample, err)
	}

	res = run(t, cfg, map[string]any{"action": "get", "id": "sherlock"})
	mustOK(t, res)
	if !strings.Contains(res.ForLLM, `"name":"Sherlock Holmes"`) {
		t.Errorf("get = %s", res.ForLLM)
	}

	res = run(t, cfg, map[string]any{"action": "update", "id": "sherlock", "changes": map[string]any{"nickname": "Holmes", "description": nil}})
	mustOK(t, res)
	if !strings.Contains(res.ForLLM, `"nickname":"Holmes"`) || strings.Contains(res.ForLLM, `"description"`) {
		t.Errorf("update = %s", res.ForLLM)
	}

	mustOK(t, run(t, cfg, map[string]any{"action": "copy", "id": "sherlock", "new_id": "watson"}))
	mustOK(t, run(t, cfg, map[string]any{"action": "rename", "id": "watson", "new_id": "mycroft"}))

	res = run(t, cfg, map[string]any{"action": "list"})
	mustOK(t, res)
	var list struct {
		Cards []struct {
			ID         string   `json:"id"`
			Nickname   string   `json:"nickname"`
			AttachedTo []string `json:"attached_to"`
		} `json:"cards"`
		Invalid []ccv3pkg.Invalid `json:"invalid"`
	}
	if err := json.Unmarshal([]byte(res.ForLLM), &list); err != nil {
		t.Fatalf("list %s: %v", res.ForLLM, err)
	}
	if len(list.Cards) != 2 || list.Cards[0].ID != "mycroft" || list.Cards[1].ID != "sherlock" {
		t.Fatalf("list cards = %+v", list.Cards)
	}
	if len(list.Cards[0].AttachedTo) != 0 || len(list.Cards[1].AttachedTo) != 1 || list.Cards[1].AttachedTo[0] != "carol" {
		t.Errorf("attached_to = %v, %v", list.Cards[0].AttachedTo, list.Cards[1].AttachedTo)
	}
	if list.Invalid == nil || !strings.Contains(res.ForLLM, `"invalid":[]`) {
		t.Errorf("invalid = %s", res.ForLLM)
	}

	mustOK(t, run(t, cfg, map[string]any{"action": "delete", "id": "mycroft"}))
	mustRefuse(t, run(t, cfg, map[string]any{"action": "get", "id": "mycroft"}), `Card "mycroft" does not exist.`)
}

func TestRefusals(t *testing.T) {
	cfg := testConfig(t)
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{ID: "dave", CharacterCard: "sherlock"})
	mustOK(t, run(t, cfg, map[string]any{"action": "create", "id": "sherlock", "card": card("Sherlock Holmes")}))
	mustOK(t, run(t, cfg, map[string]any{"action": "create", "id": "free", "card": card("Free")}))

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"no action", map[string]any{}, "The action argument is required: list, get, create, update, rename, copy or delete."},
		{"unknown action", map[string]any{"action": "fetch"}, `Unknown action "fetch"; use list, get, create, update, rename, copy or delete.`},
		{"unknown argument", map[string]any{"action": "list", "cardid": "x"}, "Unknown argument cardid; use action, id, new_id, card or changes."},
		{"argument the action does not take", map[string]any{"action": "update", "id": "free", "card": card("X")}, "The update action does not take card."},
		{"missing id", map[string]any{"action": "get"}, "The id argument is required for get."},
		{"blank id", map[string]any{"action": "delete", "id": " "}, "The id argument is required for delete."},
		{"id not text", map[string]any{"action": "get", "id": 3}, "The id argument must be text."},
		{"missing new_id", map[string]any{"action": "copy", "id": "free"}, "The new_id argument is required for copy."},
		{"missing card", map[string]any{"action": "create", "id": "x"}, "The card argument is required for create: a JSON object."},
		{"missing changes", map[string]any{"action": "update", "id": "free"}, "The changes argument is required for update: a JSON object."},
		{"invalid id", map[string]any{"action": "create", "id": "-x", "card": card("X")}, `Card id "-x" is not valid`},
		{"invalid card", map[string]any{"action": "create", "id": "x", "card": map[string]any{"name": "X", "naem": "y"}}, "naem is not a card field"},
		{"card not an object", map[string]any{"action": "create", "id": "x", "card": "text"}, "card must be an object"},
		{"changes not an object", map[string]any{"action": "update", "id": "free", "changes": []any{}}, "changes must be an object"},
		{"changes set a date", map[string]any{"action": "update", "id": "free", "changes": map[string]any{"creation_date": 1}}, "creation_date is set automatically"},
		{"exists", map[string]any{"action": "create", "id": "Free", "card": card("X")}, `Card "Free" already exists.`},
		{"not found", map[string]any{"action": "update", "id": "nope", "changes": map[string]any{"name": "N"}}, `Card "nope" does not exist.`},
		{"rename attached", map[string]any{"action": "rename", "id": "SHERLOCK", "new_id": "holmes"}, `Card "SHERLOCK" is attached to agents Carol, dave; detach it first.`},
		{"delete attached", map[string]any{"action": "delete", "id": "sherlock"}, `Card "sherlock" is attached to agents Carol, dave; detach it first.`},
		{"copy onto existing", map[string]any{"action": "copy", "id": "sherlock", "new_id": "free"}, `Card "free" already exists.`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustRefuse(t, run(t, cfg, tc.args), tc.want)
		})
	}

	// One agent: the singular form.
	cfg.Agents.List = cfg.Agents.List[:3]
	mustRefuse(t, run(t, cfg, map[string]any{"action": "delete", "id": "sherlock"}), `Card "sherlock" is attached to agent Carol; detach it first.`)
	// Nothing was renamed or deleted.
	mustOK(t, run(t, cfg, map[string]any{"action": "get", "id": "sherlock"}))
}
