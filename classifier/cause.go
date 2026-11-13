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

// cause is what a failure is down to: its kind, the role and what it names
// (the tool, or "model <name>"), a summary in ClawEh's own words, and the
// detail: text a tool, a provider or an item gave (an error message, an
// excerpt of a result). The detail is untrusted: it reaches the agent only
// inside Host.WrapUntrusted, never as part of a notice's own sentences.
type cause struct {
	kind    causeKind
	role    string // roleSource, roleFetch, roleModel, roleAction, "" for others
	subject string
	summary string
	detail  string
}

// Roles, as errors and notices name them.
const (
	roleSource = "source"
	roleFetch  = "fetch"
	roleModel  = "model"
	roleAction = "action"
)

// roleRank orders roles for a choice that must not depend on timing: the
// source first, then the fetch, the model, the action, anything else.
func roleRank(role string) int {
	switch role {
	case roleSource:
		return 0
	case roleFetch:
		return 1
	case roleModel:
		return 2
	case roleAction:
		return 3
	}
	return 4
}

// explained is an error with a summary in ClawEh's own words and a detail
// that came from outside (a tool's message, an excerpt).
type explained interface {
	summary() string
	detail() string
}

// toolError is a failed call of a tool the definition names, in its role
// (source, fetch, or the action of category). A tool the agent cannot use
// is named by the host's own sentence (no detail); any other failure is
// "fetch mail_read: the tool failed" with the tool's message as the detail.
type toolError struct {
	role     string
	category string // the action's category, "" otherwise
	tool     string
	err      error
}

func (e *toolError) unavailable() bool { return errors.Is(e.err, ErrToolUnavailable) }

func (e *toolError) summary() string {
	if e.unavailable() {
		return roleLabel(e.role, e.category) + ": " + strings.TrimSuffix(e.err.Error(), ".")
	}
	return e.role + " " + e.tool + ": the tool failed"
}

func (e *toolError) detail() string {
	if e.unavailable() {
		return ""
	}
	return e.err.Error()
}

func (e *toolError) Error() string { return joinDetail(e) }
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
// "model cheap: the call failed", with the provider's message as the
// detail.
type modelError struct {
	model string
	err   error
}

func (e *modelError) summary() string {
	if e.model == "" {
		return "the classifier model: the call failed"
	}
	return "model " + e.model + ": the call failed"
}

// detail is the provider's message without the host's "model <name>: ".
func (e *modelError) detail() string {
	return strings.TrimPrefix(e.err.Error(), "model "+e.model+": ")
}

func (e *modelError) Error() string { return joinDetail(e) }
func (e *modelError) Unwrap() error { return e.err }

// notJSONError is a tool result that is not JSON: the parse error and what
// came back are the detail.
type notJSONError struct {
	role, tool string
	parseErr   error
	result     string
}

func (e *notJSONError) summary() string { return e.role + " " + e.tool + ": the result is not JSON" }

func (e *notJSONError) detail() string {
	return fmt.Sprintf("%v; it returned %s", e.parseErr, excerpt(e.result))
}
func (e *notJSONError) Error() string { return joinDetail(e) }
func (e *notJSONError) Unwrap() error { return e.parseErr }

// noFieldsError is a fetch result in which no fetch field resolved, naming
// the missing fields; notJSON is set when the result is not JSON.
type noFieldsError struct {
	tool    string
	missing []string
	notJSON *notJSONError
}

func (e *noFieldsError) summary() string {
	missing := strings.Join(e.missing, ", ")
	if e.notJSON != nil {
		return fmt.Sprintf("%s %s: the result is not JSON, so no fetch field resolved (missing: %s)", roleFetch, e.tool, missing)
	}
	return fmt.Sprintf("%s %s: no fetch field resolved (missing: %s)", roleFetch, e.tool, missing)
}

func (e *noFieldsError) detail() string {
	if e.notJSON == nil {
		return ""
	}
	return e.notJSON.detail()
}

func (e *noFieldsError) Error() string { return joinDetail(e) }

// joinDetail is an explained error's full text: the summary, then the
// detail when there is one. It is what the activity log and the log show.
func joinDetail(e explained) string {
	if d := e.detail(); d != "" {
		return e.summary() + ": " + d
	}
	return e.summary()
}

// causeOf is what err is down to, with its summary and detail.
func causeOf(err error) cause {
	c := cause{kind: causeDefinition, summary: err.Error()}
	var ex explained
	if errors.As(err, &ex) {
		c.summary, c.detail = ex.summary(), ex.detail()
	}
	var te *toolError
	var me *modelError
	var nj *notJSONError
	var nf *noFieldsError
	switch {
	case errors.Is(err, ErrNoModel):
		c.kind, c.role = causeNoModel, roleModel
	case errors.Is(err, ErrStateUnavailable):
		c.kind = causeOperator
	case errors.As(err, &te) && te.unavailable():
		c.kind, c.role, c.subject = causeToolMissing, te.role, te.tool
	case errors.As(err, &te):
		c.kind, c.role, c.subject = causeTool, te.role, te.tool
	case errors.As(err, &me):
		c.kind, c.role, c.subject = causeTool, roleModel, "the classifier model"
		if me.model != "" {
			c.subject = "model " + me.model
		}
	case errors.As(err, &nj):
		c.role, c.subject = nj.role, nj.tool
	case errors.As(err, &nf):
		c.role, c.subject = roleFetch, nf.tool
	}
	return c
}

// failureAdvice follows the failure in the agent's notice when the
// definition or its instructions files are to fix.
const failureAdvice = "Check and fix the classifier's definition or its instructions files; classifier_test shows the result."

// advice is the sentence after the failure in the agent's notice: what to
// do about a failure of this cause.
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

// commonCause is the cause most of causes share (by summary), ties going
// to the earlier role (roleRank) and then the summary's order, so the
// choice does not depend on which item finished first.
func commonCause(causes []cause) cause {
	counts := map[string]int{}
	var best cause
	bestN := 0
	for _, c := range causes {
		counts[c.summary]++
	}
	for _, c := range causes {
		n := counts[c.summary]
		switch {
		case n > bestN,
			n == bestN && roleRank(c.role) < roleRank(best.role),
			n == bestN && roleRank(c.role) == roleRank(best.role) && c.summary < best.summary:
			best, bestN = c, n
		}
	}
	return best
}

// failurePlural is "1 consecutive item failure" or "N consecutive item
// failures".
func failurePlural(n int) string {
	if n == 1 {
		return "1 consecutive item failure"
	}
	return fmt.Sprintf("%d consecutive item failures", n)
}
