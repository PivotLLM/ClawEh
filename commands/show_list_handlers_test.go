package commands

import (
	"context"
	"strings"
	"testing"
)

func TestShowListHandlers_ChannelPolicy(t *testing.T) {
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), nil)

	var telegramReply string
	handled := ex.Execute(context.Background(), Request{
		Channel: "telegram",
		Text:    "/show channel",
		Reply: func(text string) error {
			telegramReply = text
			return nil
		},
	})
	if handled.Outcome != OutcomeHandled {
		t.Fatalf("telegram /show outcome=%v, want=%v", handled.Outcome, OutcomeHandled)
	}
	if telegramReply != "Current Channel: telegram" {
		t.Fatalf("telegram /show reply=%q, want=%q", telegramReply, "Current Channel: telegram")
	}

	var slackReply string
	handledSlack := ex.Execute(context.Background(), Request{
		Channel: "slack",
		Text:    "/show channel",
		Reply: func(text string) error {
			slackReply = text
			return nil
		},
	})
	if handledSlack.Outcome != OutcomeHandled {
		t.Fatalf("slack /show outcome=%v, want=%v", handledSlack.Outcome, OutcomeHandled)
	}
	if handledSlack.Command != "show" {
		t.Fatalf("slack /show command=%q, want=%q", handledSlack.Command, "show")
	}
	if slackReply != "Current Channel: slack" {
		t.Fatalf("slack /show reply=%q, want=%q", slackReply, "Current Channel: slack")
	}

	passthrough := ex.Execute(context.Background(), Request{
		Channel: "slack",
		Text:    "/foo",
	})
	if passthrough.Outcome != OutcomePassthrough {
		t.Fatalf("slack /foo outcome=%v, want=%v", passthrough.Outcome, OutcomePassthrough)
	}
	if passthrough.Command != "foo" {
		t.Fatalf("slack /foo command=%q, want=%q", passthrough.Command, "foo")
	}
}

func TestShowListHandlers_ListHandledOnAllChannels(t *testing.T) {
	rt := &Runtime{
		GetEnabledChannels: func() []string {
			return []string{"telegram"}
		},
	}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	res := ex.Execute(context.Background(), Request{
		Channel: "slack",
		Text:    "/list channels",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if res.Outcome != OutcomeHandled {
		t.Fatalf("slack /list outcome=%v, want=%v", res.Outcome, OutcomeHandled)
	}
	if res.Command != "list" {
		t.Fatalf("slack /list command=%q, want=%q", res.Command, "list")
	}
	if !strings.Contains(reply, "telegram") {
		t.Fatalf("slack /list reply=%q, expected enabled channels content", reply)
	}
}
