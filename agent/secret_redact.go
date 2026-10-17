// ClawEh
// License: MIT

package agent

import (
	"regexp"
	"strings"
	"unicode"
)

// secretRedaction replaces secret-looking text in provider error messages.
const secretRedaction = "[REDACTED]"

var (
	// secretPrefixRe matches API keys by their vendor prefix: OpenAI and
	// Anthropic (sk-, sk-ant-), xAI (xai-), Groq (gsk_), Google (AIza) and AWS
	// access key ids (AKIA).
	secretPrefixRe = regexp.MustCompile(`\b(?:sk-|xai-|gsk_)[A-Za-z0-9_\-]{8,}|\bAIza[A-Za-z0-9_\-]{20,}|\bAKIA[0-9A-Z]{16}\b`)
	// authHeaderRe matches the credential of a Bearer or Basic authorization
	// (16 characters or more, so "basic validation" is not taken for one).
	authHeaderRe = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9._~+/\-]{16,}=*`)
	// credentialRe matches a credential-named field and its value, in the
	// forms name=value, name: value and "name": "value". Group 1 is the
	// character before the name (the name must stand alone, so "primary
	// key=…" and "monkey=…" are not matched), group 2 the name and separator,
	// group 3 the value.
	credentialRe = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_-])((?:x-api-key|api[ _-]?key(?: provided)?|apikey|access[_-]?token|auth[_-]?token|client[_-]?secret|token|secret|password)"?\s*[:=]\s*"?)([^\s"'&,;]+)`)
	// longRunRe matches a run of 32 or more base64 letters (no "/", "-" or
	// "_", so a path, a model id or a UUID is never one run). Group 1 is an
	// id prefix such as "req_": a run carrying one is a request id, kept for
	// support.
	longRunRe = regexp.MustCompile(`(\b[A-Za-z]{2,10}_)?[A-Za-z0-9+]{32,}={0,2}`)
)

// redactSecrets replaces API keys, authorization credentials, the values of
// credential-named fields and long key-like runs in s with secretRedaction.
// It is applied to provider error text before it reaches a chat or the log.
// Model ids, UUIDs and prefixed request ids (req_…) are left alone.
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = secretPrefixRe.ReplaceAllString(s, secretRedaction)
	s = authHeaderRe.ReplaceAllString(s, "${1}${2}"+secretRedaction)
	s = replaceSubmatches(s, credentialRe, func(m []string) string {
		if len(m[3]) < 8 || !lettersAndDigits(m[3]) {
			return m[0]
		}
		return m[1] + m[2] + secretRedaction
	})
	return replaceSubmatches(s, longRunRe, func(m []string) string {
		if m[1] != "" || !lettersAndDigits(m[0]) {
			return m[0]
		}
		return secretRedaction
	})
}

// replaceSubmatches replaces every match of re in s with repl's result for
// the match and its groups (an unmatched group is "").
func replaceSubmatches(s string, re *regexp.Regexp, repl func(m []string) string) string {
	idx := re.FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range idx {
		m := make([]string, len(loc)/2)
		for i := range m {
			if loc[2*i] >= 0 {
				m[i] = s[loc[2*i]:loc[2*i+1]]
			}
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(repl(m))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// lettersAndDigits reports whether s has at least one letter and one digit.
func lettersAndDigits(s string) bool {
	var letter, digit bool
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsLetter(r):
			letter = true
		}
	}
	return letter && digit
}

// redactedError returns err's text with secrets redacted, for a log field, or
// nil for a nil error.
func redactedError(err error) any {
	if err == nil {
		return nil
	}
	return redactSecrets(err.Error())
}
