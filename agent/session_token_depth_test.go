// ClawEh
// License: MIT

package agent

import (
	"context"
	"slices"
	"testing"

	"github.com/PivotLLM/ClawEh/providers"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// Every turn records its sub-agent depth on the session's token, so a CLI
// provider's MCP tool calls run at it: a raised turn records its depth and the
// following ordinary turn records 0 again.
func TestRunAgentLoop_RecordsTurnDepthOnSessionToken(t *testing.T) {
	al := newTestAgentLoop(t).al
	agentInstance := al.registry.Default()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	rec := &recordingSTI{}
	al.SetSessionTokenIssuer(rec)
	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{{Content: "one"}, {Content: "two"}},
		errors:    []error{nil, nil},
	}

	opts := processOptions{SessionKey: "agent:main:main", Channel: "slack", ChatID: "C1", UserMessage: "hi"}
	if _, err := al.runAgentLoop(toolsagents.WithSpawnDepth(context.Background(), 2), agentInstance, opts); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if _, err := al.runAgentLoop(context.Background(), agentInstance, opts); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	var got []int
	for _, c := range rec.depthCalls() {
		if c.sessionKey == opts.SessionKey {
			got = append(got, c.depth)
		}
	}
	if !slices.Equal(got, []int{2, 0}) {
		t.Errorf("recorded depths = %v, want [2 0]", got)
	}
}

// A sub-agent clone's session token carries its parent's depth + 1, although
// its turn runs on the internal "subagent" channel.
func TestRunSubagentTask_CloneTokenCarriesParentDepthPlusOne(t *testing.T) {
	provider := &sequenceProvider{
		responses: []*providers.LLMResponse{{Content: "done"}},
		errors:    []error{nil},
	}
	al := newSubagentTestLoop(t, provider)
	rec := &recordingSTI{}
	al.SetSessionTokenIssuer(rec)

	res, release, err := al.runSubagentTask(toolsagents.WithSpawnDepth(context.Background(), 1), "main", "do it", "", nil)
	defer release()
	if err != nil {
		t.Fatalf("runSubagentTask: %v", err)
	}
	for _, c := range rec.depthCalls() {
		if c.sessionKey == res.SessionKey {
			if c.depth != 2 {
				t.Errorf("clone token depth = %d, want 2", c.depth)
			}
			return
		}
	}
	t.Fatalf("no depth recorded for the clone's session %q; got %+v", res.SessionKey, rec.depthCalls())
}
