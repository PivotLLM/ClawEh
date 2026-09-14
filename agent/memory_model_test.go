// ClawEh - Cognitive Memory
// License: MIT

package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/PivotLLM/cogmem/consolidate"
)

// The adapter must hand the request through unchanged — including Exclude,
// which is how cogmem steers away from a model that returned garbage — and
// report which model answered, which is what the run record names.
func TestMemoryModelCaller_PassesRequestThroughAndNamesModel(t *testing.T) {
	var got struct {
		system, user string
		jsonObject   bool
		exclude      []string
	}
	c := &memoryModelCaller{
		modelName: "chain-head",
		complete: func(_ context.Context, system, user string, jsonObject bool, exclude []string) (string, string, string, error) {
			got.system, got.user, got.jsonObject, got.exclude = system, user, jsonObject, exclude
			return `{"domain_ops":[]}`, "stop", "second-model", nil
		},
	}
	reply, err := c.Complete(context.Background(), consolidate.ModelRequest{
		System: "sys", User: "{}", JSONObject: true, Exclude: []string{"first-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.system != "sys" || got.user != "{}" || !got.jsonObject || len(got.exclude) != 1 || got.exclude[0] != "first-model" {
		t.Fatalf("request not passed through: %+v", got)
	}
	if reply.Model != "second-model" || reply.FinishReason != "stop" || reply.Content != `{"domain_ops":[]}` {
		t.Fatalf("reply = %+v", reply)
	}
	if c.ModelName() != "chain-head" {
		t.Fatalf("ModelName = %q", c.ModelName())
	}
}

// A host error (the chain exhausted) reaches cogmem as an error with the last
// model tried, so the run record can still attribute the failure.
func TestMemoryModelCaller_HostErrorSurfaces(t *testing.T) {
	c := &memoryModelCaller{complete: func(context.Context, string, string, bool, []string) (string, string, string, error) {
		return "", "", "last-tried", errors.New("no model available")
	}}
	reply, err := c.Complete(context.Background(), consolidate.ModelRequest{})
	if err == nil || reply.Model != "last-tried" {
		t.Fatalf("reply=%+v err=%v", reply, err)
	}
}
