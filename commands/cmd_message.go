package commands

import (
	"context"
	"strings"
	"unicode"
)

func askCommand() Definition {
	return Definition{
		Name:        "ask",
		Description: "Ask another agent and post its reply here",
		Usage:       "/ask <agent> <message>",
		Handler: func(ctx context.Context, req Request, rt *Runtime) error {
			return runMessageCommand(req, "/ask <agent> <message>", rt != nil && rt.AskAgent != nil, func(agent, text string) string {
				return rt.AskAgent(ctx, agent, text)
			})
		},
	}
}

func whisperCommand() Definition {
	return Definition{
		Name:        "whisper",
		Description: "Leave a private note for another agent's next message",
		Usage:       "/whisper <agent> <message>",
		Handler: func(ctx context.Context, req Request, rt *Runtime) error {
			return runMessageCommand(req, "/whisper <agent> <message>", rt != nil && rt.WhisperAgent != nil, func(agent, text string) string {
				return rt.WhisperAgent(ctx, agent, text)
			})
		},
	}
}

// runMessageCommand parses "<agent> <message>" after the command and hands
// it to send, replying with what send returns (nothing when empty).
func runMessageCommand(req Request, usage string, available bool, send func(agent, text string) string) error {
	if !available {
		return req.Reply(unavailableMsg)
	}
	agent, text := splitAgentAndText(req.Text)
	if agent == "" || text == "" {
		return req.Reply("Usage: " + usage)
	}
	if reply := send(agent, text); reply != "" {
		return req.Reply(reply)
	}
	return nil
}

// splitAgentAndText splits "/cmd <agent> <text>" into the agent and the rest
// of the text, which keeps its own spacing and line breaks.
func splitAgentAndText(input string) (agent, text string) {
	_, rest := cutToken(strings.TrimSpace(input))
	agent, text = cutToken(rest)
	return agent, text
}

// cutToken returns the first whitespace-separated token of s and the rest,
// trimmed at the start.
func cutToken(s string) (token, rest string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}
