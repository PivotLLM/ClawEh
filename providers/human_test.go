// ClawEh
// License: MIT

package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
)

func TestHumanProvider_FromFactory(t *testing.T) {
	model := &config.ModelConfig{ModelName: "Bob (human)", Model: "bob", RequestTimeout: 90}
	p, id, err := CreateProviderFromConfig(model, &config.Provider{Name: "People", Protocol: config.HumanProtocol})
	if err != nil {
		t.Fatalf("CreateProviderFromConfig: %v", err)
	}
	hp, ok := p.(*HumanProvider)
	if !ok {
		t.Fatalf("provider is %T, want *HumanProvider", p)
	}
	if id != "bob" || hp.Timeout() != 90*time.Second || hp.GetDefaultModel() != "Bob (human)" {
		t.Errorf("got id %q, timeout %s, label %q", id, hp.Timeout(), hp.GetDefaultModel())
	}
}

// Outside a human agent's turn there is nobody to ask: the provider refuses.
func TestHumanProvider_RefusesWithoutRelay(t *testing.T) {
	_, err := NewHumanProvider("Bob (human)", 0).Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "", nil)
	if !errors.Is(err, ErrHumanUnreachable) {
		t.Fatalf("err = %v, want ErrHumanUnreachable", err)
	}
}

// The person sees only the latest user message, never the system prompt or
// the history, and their answer is the reply.
func TestHumanProvider_RelaysLatestUserMessage(t *testing.T) {
	var asked []string
	relay := func(_ context.Context, request string, timeout time.Duration) (string, error) {
		if timeout != time.Minute {
			t.Errorf("relay got timeout %s, want the provider's", timeout)
		}
		asked = append(asked, request)
		return "Looks good", nil
	}
	msgs := []Message{
		{Role: "system", Content: "You are Bob."},
		{Role: "user", Content: "old question"},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "[From: Alice]\nPlease review the draft."},
	}
	resp, err := NewHumanProvider("Bob (human)", time.Minute).Chat(WithHumanRelay(context.Background(), relay), msgs, nil, "bob", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(asked) != 1 || asked[0] != "[From: Alice]\nPlease review the draft." {
		t.Fatalf("relayed %q, want only the latest user message", asked)
	}
	if resp.Content != "Looks good" {
		t.Errorf("reply = %q, want the person's answer", resp.Content)
	}
}

// When the person does not answer within the timeout the reply is empty, not
// an error; the turn's own end is still an error.
func TestHumanProvider_Timeout(t *testing.T) {
	// The relay applies the timeout once the request is posted.
	wait := func(ctx context.Context, _ string, timeout time.Duration) (string, error) {
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		<-waitCtx.Done()
		return "", waitCtx.Err()
	}
	msgs := []Message{{Role: "user", Content: "hi"}}

	resp, err := NewHumanProvider("Bob (human)", 20*time.Millisecond).Chat(WithHumanRelay(context.Background(), wait), msgs, nil, "", nil)
	if err != nil || resp == nil || resp.Content != "" {
		t.Fatalf("timeout: resp %+v, err %v; want an empty reply", resp, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewHumanProvider("Bob (human)", time.Minute).Chat(WithHumanRelay(ctx, wait), msgs, nil, "", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled turn: err = %v, want context.Canceled", err)
	}
}
