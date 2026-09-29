package telegram

import "regexp"

// botTokenRe matches a bot token as it appears in a Telegram Bot API URL
// (https://api.telegram.org/bot<id>:<secret>/getUpdates, and the same under a
// custom API server or the /file/ download path). telego only accepts tokens
// of this shape, so every URL it builds carries the token this way.
var botTokenRe = regexp.MustCompile(`bot\d+:[\w-]+`)

// RedactToken replaces every bot token in s with "bot<redacted>".
func RedactToken(s string) string {
	return botTokenRe.ReplaceAllString(s, "bot<redacted>")
}

// redactedError is an error whose text has had bot tokens removed. It unwraps
// to the original so errors.Is and errors.As still classify it.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redactErr returns err with any bot token removed from its text. telego's
// transport errors embed the request URL, which carries the token; every
// telego error goes through here before it is logged, alerted or returned.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	red := RedactToken(msg)
	if red == msg {
		return err
	}
	return &redactedError{msg: red, err: err}
}
