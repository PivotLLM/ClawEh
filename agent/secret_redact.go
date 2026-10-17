// ClawEh
// License: MIT

package agent

import (
	"regexp"
	"unicode"
)

// secretRedaction replaces secret-looking text in provider error messages.
const secretRedaction = "[REDACTED]"

var (
	// secretPrefixRe matches API keys by their vendor prefix: OpenAI and
	// Anthropic (sk-, sk-ant-), xAI (xai-), Groq (gsk_) and Google (AIza).
	secretPrefixRe = regexp.MustCompile(`\b(?:sk-|xai-|gsk_)[A-Za-z0-9_\-]{8,}|\bAIza[A-Za-z0-9_\-]{20,}`)
	// authHeaderRe matches the credential of a Bearer or Basic authorization
	// (16 characters or more, so "basic validation" is not taken for one).
	authHeaderRe = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9._~+/\-]{16,}=*`)
	// secretParamRe matches the value of a key=, token=, api_key=, … parameter.
	secretParamRe = regexp.MustCompile(`(?i)\b((?:api[_-]?key|access[_-]?token|secret|password|token|key)\s*=\s*)[^\s"'&,;]+`)
	// longRunRe matches a long hex or base64 run; only one mixing letters and
	// digits is redacted, so a long ordinary word is left alone.
	longRunRe = regexp.MustCompile(`[A-Za-z0-9+/_\-]{32,}={0,2}`)
)

// redactSecrets replaces API keys, authorization credentials, secret-named
// parameter values and long key-like runs in s with secretRedaction. It is
// applied to provider error text before it reaches a chat or the log.
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = secretPrefixRe.ReplaceAllString(s, secretRedaction)
	s = authHeaderRe.ReplaceAllString(s, "${1}${2}"+secretRedaction)
	s = secretParamRe.ReplaceAllString(s, "${1}"+secretRedaction)
	return longRunRe.ReplaceAllStringFunc(s, func(run string) string {
		var letter, digit bool
		for _, r := range run {
			switch {
			case unicode.IsDigit(r):
				digit = true
			case unicode.IsLetter(r):
				letter = true
			}
		}
		if letter && digit {
			return secretRedaction
		}
		return run
	})
}

// redactedError returns err's text with secrets redacted, for a log field, or
// nil for a nil error.
func redactedError(err error) any {
	if err == nil {
		return nil
	}
	return redactSecrets(err.Error())
}
