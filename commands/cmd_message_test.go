package commands

import (
	"context"
	"testing"
)

// TestMessageCommands: /ask and /whisper hand the agent and the rest of the
// text (spacing kept) to the host and reply with what it returns; a missing
// agent or message gets the usage, and a host without support "unavailable".
func TestMessageCommands(t *testing.T) {
	for _, tc := range []struct {
		name, text, wantAgent, wantText, wantReply string
		hostReply                                  string
		noHost                                     bool
	}{
		{name: "ask", text: "/ask bob what is  up?\nthanks", wantAgent: "bob", wantText: "what is  up?\nthanks"},
		{name: "ask refused", text: "/ask bob hi", wantAgent: "bob", wantText: "hi", hostReply: "You don't have permission to /ask Bob.", wantReply: "You don't have permission to /ask Bob."},
		{name: "whisper", text: "/whisper@bot alice psst", wantAgent: "alice", wantText: "psst", hostReply: "Whispered to Alice.", wantReply: "Whispered to Alice."},
		{name: "no message", text: "/ask bob", wantReply: "Usage: /ask <agent> <message>"},
		{name: "no agent", text: "/whisper", wantReply: "Usage: /whisper <agent> <message>"},
		{name: "no host", text: "/ask bob hi", noHost: true, wantReply: unavailableMsg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotAgent, gotText, reply string
			host := func(_ context.Context, agent, text string) string {
				gotAgent, gotText = agent, text
				return tc.hostReply
			}
			rt := &Runtime{AskAgent: host, WhisperAgent: host}
			if tc.noHost {
				rt = &Runtime{}
			}
			ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)
			res := ex.Execute(context.Background(), Request{Text: tc.text, Reply: func(s string) error { reply = s; return nil }})
			if res.Outcome != OutcomeHandled || res.Err != nil {
				t.Fatalf("result = %+v", res)
			}
			if gotAgent != tc.wantAgent || gotText != tc.wantText || reply != tc.wantReply {
				t.Fatalf("host got (%q, %q), reply %q; want (%q, %q), %q", gotAgent, gotText, reply, tc.wantAgent, tc.wantText, tc.wantReply)
			}
		})
	}
}
