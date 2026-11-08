package schedule

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/cron"
	"github.com/PivotLLM/ClawEh/tools"
)

// probeTool answers a watch probe with result.
type probeTool struct {
	name   string
	result *tools.ToolResult
	scoped bool
	sawCtx context.Context
}

func (p *probeTool) Name() string               { return p.name }
func (p *probeTool) Description() string        { return "probe" }
func (p *probeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (p *probeTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	p.sawCtx = ctx
	return p.result
}
func (p *probeTool) IsSessionScoped() bool { return p.scoped }

// A watch probe runs the agent's tool unattended: its output on success,
// the tool's own text on failure, a refusal naming the tool when the agent
// lacks it or it needs a session, and no chat to send to.
func TestWatchProbe_Outcomes(t *testing.T) {
	ok := &probeTool{name: "mail_list", result: tools.NewToolResult(`{"ids":[1]}`)}
	failing := &probeTool{name: "mail_fail", result: tools.ErrorResult("mailbox locked")}
	scoped := &probeTool{name: "session_x", result: tools.NewToolResult("x"), scoped: true}
	reg := tools.NewToolRegistry()
	for _, tl := range []tools.Tool{ok, failing, scoped} {
		reg.Register(tl)
	}
	ct := &CronTool{}
	ct.agentTools = func(agentID string) *tools.ToolRegistry {
		if agentID == "alice" {
			return reg
		}
		return nil
	}

	if out, err := ct.probe(t.Context(), "alice", &cron.CronWatch{Tool: "mail_list"}); err != nil || out != `{"ids":[1]}` {
		t.Fatalf("probe = %q, %v", out, err)
	}
	if _, problem := tools.TurnChat(ok.sawCtx); problem != tools.ChatNone {
		t.Fatalf("probe ran with a turn chat (%v), want none", problem)
	}
	for _, tt := range []struct {
		agent, tool, want string
	}{
		{"alice", "mail_fail", `tool "mail_fail" failed: mailbox locked`},
		{"alice", "missing", `tool "missing" is not available to agent "alice"`},
		{"alice", "session_x", `tool "session_x" is session-scoped and cannot be used as a watch probe`},
		{"bob", "mail_list", `agent "bob" has no tool registry`},
	} {
		if _, err := ct.probe(t.Context(), tt.agent, &cron.CronWatch{Tool: tt.tool}); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("probe(%s, %s) = %v, want %q", tt.agent, tt.tool, err, tt.want)
		}
	}
}
