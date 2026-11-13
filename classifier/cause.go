// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// causeKind is what a failure is down to, which decides the advice of the
// agent's notice.
type causeKind int

const (
	causeDefinition  causeKind = iota // the definition or its instructions files
	causeTool                         // a tool or the model failed when called (credentials, an outage)
	causeToolMissing                  // a tool the definition names is missing or refused
	causeNoModel                      // no usable classifier model: the operator's Task models page
	causeOperator                     // ClawEh's own state: only the operator can fix it
	causeInvalidFile                  // the definition file is not valid, so it is not run
)

// cause is a failure's kind and what it names: the tool, or "model <name>".
type cause struct {
	kind    causeKind
	subject string
}

// Tool roles, as errors and notices name them.
const (
	roleSource = "source"
	roleFetch  = "fetch"
	roleAction = "action"
)

// toolError is a failed call of a tool the definition names, in its role
// (source, fetch, or the action of category). A tool the agent cannot use
// is named by the host's own sentence; any other failure names the role and
// the tool once: "fetch mail_read failed: <reason>".
type toolError struct {
	role     string
	category string // the action's category, "" otherwise
	tool     string
	err      error
}

func (e *toolError) Error() string {
	if errors.Is(e.err, ErrToolUnavailable) {
		return roleLabel(e.role, e.category) + ": " + strings.TrimSuffix(e.err.Error(), ".")
	}
	return fmt.Sprintf("%s %s failed: %v", e.role, e.tool, e.err)
}

func (e *toolError) Unwrap() error { return e.err }

// roleLabel names a tool's role: "source", "fetch", or "action for
// category urgent".
func roleLabel(role, category string) string {
	if category == "" {
		return role
	}
	return role + " for category " + category
}

// modelError is a failed model call (not a cooldown and not ErrNoModel):
// its text is the host's, which names the model.
type modelError struct {
	model string
	err   error
}

func (e *modelError) Error() string { return e.err.Error() }
func (e *modelError) Unwrap() error { return e.err }

// causeOf is what err is down to.
func causeOf(err error) cause {
	var te *toolError
	var me *modelError
	switch {
	case errors.Is(err, ErrNoModel):
		return cause{kind: causeNoModel}
	case errors.Is(err, ErrStateUnavailable):
		return cause{kind: causeOperator}
	case errors.As(err, &te) && errors.Is(te.err, ErrToolUnavailable):
		return cause{kind: causeToolMissing, subject: te.tool}
	case errors.As(err, &te):
		return cause{kind: causeTool, subject: te.tool}
	case errors.As(err, &me):
		subject := "the classifier model"
		if me.model != "" {
			subject = "model " + me.model
		}
		return cause{kind: causeTool, subject: subject}
	}
	return cause{kind: causeDefinition}
}

// failureAdvice follows the failure in the agent's notice when the
// definition or its instructions files are to fix.
const failureAdvice = "Check and fix the classifier's definition or its instructions files; classifier_test shows the result."

// advice is the sentence after the error in the agent's notice: what to do
// about a failure of this cause.
func advice(c cause) string {
	switch c.kind {
	case causeDefinition:
		return failureAdvice
	case causeTool:
		return "This looks like a problem with " + c.subject + " itself (credentials or an outage); check it before changing the classifier."
	case causeToolMissing:
		return "If " + c.subject + " should be available, tell the user; otherwise change the classifier."
	case causeNoModel:
		return "Only the operator can fix this on the Task models page; tell the user."
	case causeOperator:
		return "Only the operator can fix this; tell the user."
	case causeInvalidFile:
		return "Fix it with classifier_update, or delete it and create it again."
	}
	return failureAdvice
}

// excerptChars is how much of a result that is not JSON an error quotes.
const excerptChars = 200

// excerpt is the start of s, quoted, for an error: at most excerptChars
// characters, "…" marking a cut.
func excerpt(s string) string {
	q := strconv.Quote(cutRunes(s, excerptChars))
	if utf8.RuneCountInString(s) > excerptChars {
		q += "…"
	}
	return q
}

// notJSON is the error text of a tool result that is not JSON: the role
// and tool, the parse error and what came back.
func notJSON(role, tool string, parseErr error, result string) string {
	return fmt.Sprintf("%s %s did not return JSON (%v); it returned %s", role, tool, parseErr, excerpt(result))
}
