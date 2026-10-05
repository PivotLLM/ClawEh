// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/providers"
)

func TestWithJitter_WithinBounds(t *testing.T) {
	base := 5 * time.Second
	lo := time.Duration(float64(base) * (1 - backoffJitterFraction))
	hi := time.Duration(float64(base) * (1 + backoffJitterFraction))
	for range 1000 {
		got := withJitter(base)
		if got < lo || got > hi {
			t.Fatalf("withJitter(%v) = %v, outside [%v, %v]", base, got, lo, hi)
		}
	}
}

// TestClassifyLLMError pins the buckets the retry loop acted on before the
// spawnllm classifier replaced the local string matching.
func TestClassifyLLMError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantTimeout bool
		wantContext bool
	}{
		// Timeouts (retry after backoff), never treated as context errors.
		{"deadline exceeded sentinel", context.DeadlineExceeded, true, false},
		{"wrapped deadline", fmt.Errorf("dispatch: %w", context.DeadlineExceeded), true, false},
		{"client timeout", errors.New("Post \"https://x\": net/http: request canceled (Client.Timeout exceeded while awaiting headers)"), true, false},
		{"timed out", errors.New("request timed out"), true, false},
		{"timeout exceeded", errors.New("timeout exceeded"), true, false},
		{"timeout mentioning context window", errors.New("timed out while assembling context window"), true, false},

		// Context overflow (compress and retry): patterns spawnllm classifies.
		{"context_length_exceeded", errors.New("error: context_length_exceeded"), false, true},
		{"maximum context length", errors.New("This model's maximum context length is 8192 tokens"), false, true},
		{"too many tokens", errors.New("too many tokens in request"), false, true},
		{"prompt is too long", errors.New("prompt is too long: 210000 tokens > 200000 maximum"), false, true},
		{"request too large", errors.New("request too large for model"), false, true},

		// Context overflow: patterns only the local list classifies.
		{"context window", errors.New("exceeds the context window of the model"), false, true},
		{"token limit", errors.New("token limit reached"), false, true},
		{"max_tokens", errors.New("max_tokens exceeds the model limit"), false, true},
		{"invalidparameter", errors.New("InvalidParameter: Total tokens of image and text exceed max message tokens"), false, true},

		// Fallback chain exhausted with every candidate at its context limit.
		{"fallback all context limit", &providers.FallbackExhaustedError{Attempts: []providers.FallbackAttempt{
			{Provider: "p", Model: "m", Error: errors.New("boom"), Reason: providers.FailoverContextLimit},
		}}, false, true},
		{"fallback mixed reasons", &providers.FallbackExhaustedError{Attempts: []providers.FallbackAttempt{
			{Provider: "p", Model: "m", Error: errors.New("boom"), Reason: providers.FailoverAuth},
		}}, false, false},

		// Neither: returned to the caller unchanged.
		{"auth", errors.New("401 unauthorized"), false, false},
		{"canceled", context.Canceled, false, false},
		{"unknown", errors.New("network is unreachable"), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTimeout, gotContext := classifyLLMError(tt.err, "prov", "model")
			if gotTimeout != tt.wantTimeout || gotContext != tt.wantContext {
				t.Fatalf("classifyLLMError(%q) = (timeout=%v, context=%v), want (%v, %v)",
					tt.err, gotTimeout, gotContext, tt.wantTimeout, tt.wantContext)
			}
		})
	}
}
