// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// classifier_test and classifier_rerun pass a failing source through with
// its real reason, the one every failure notice tells the agent to use; the
// state that cannot be opened is a plain refusal too.
func TestToolsPassSourceFailuresThrough(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	defs := Tools(e.svc, fakeToolHost{scope: e.scope})
	const reason = "Graph API error 401: InvalidAuthenticationToken: Access token has expired."
	e.failingSource(reason)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"test", map[string]any{"id": "inbox-priority"}},
		{"test", map[string]any{"id": "inbox-priority", "item_id": "m1"}},
		{"rerun", map[string]any{"id": "inbox-priority", "item_id": "m1"}},
	} {
		res := call(t, defs, tc.tool, tc.args)
		if want := "source mail_list failed: " + reason; !res.IsError || res.ForLLM != want {
			t.Errorf("%s %v = %q, want %q", tc.tool, tc.args, res.ForLLM, want)
		}
	}
	e.tools.set("mail_list", func(map[string]any) (string, error) { return "Hello, not JSON", nil })
	res := call(t, defs, "test", map[string]any{"id": "inbox-priority"})
	if want := `source mail_list did not return JSON (invalid character 'H' looking for beginning of value); it returned "Hello, not JSON".`; !res.IsError || res.ForLLM != want {
		t.Errorf("test on a source that is not JSON = %q, want %q", res.ForLLM, want)
	}
}

// A source result that is not JSON is named with the parse error and the
// first 200 characters of what came back, marked when cut.
func TestSourceNotJSONQuotesTheResult(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	long := "<html>" + strings.Repeat("x", 300)
	e.tools.set("mail_list", func(map[string]any) (string, error) { return long, nil })
	rec := e.run("inbox-priority")
	want := "source mail_list did not return JSON (invalid character '<' looking for beginning of value); it returned " +
		`"` + cutRunes(long, 200) + `"…`
	if rec.Status != RunError || rec.Error != want {
		t.Fatalf("run error = %q, want %q", rec.Error, want)
	}
	agent, _ := e.notices()
	if len(agent) != 1 || !strings.HasSuffix(agent[0], failureAdvice) {
		t.Errorf("notice = %q, want the definition advice", agent)
	}
}

// An item left unclassified keeps both replies of the model on its activity
// row, and classifier_test shows them.
func TestUnclassifiedKeepsReplies(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"m1"})
	e.create(definition())
	replies := []string{"I think this is important", strings.Repeat("maybe ", 200)}
	n := 0
	e.model.setReply(func(string, string) (string, error) {
		r := replies[n%2]
		n++
		return r, nil
	})
	if rec := e.run("inbox-priority"); rec.Unclassified != 1 {
		t.Fatalf("run = %+v", rec)
	}
	want := []string{replies[0], cutRunes(replies[1], ActivityValueMax)}
	rows := e.activity(ActivityQuery{})
	if len(rows) != 1 || rows[0].Outcome != OutcomeUnclassified || !slices.Equal(rows[0].Replies, want) {
		t.Fatalf("activity = %+v, want replies %q", rows, want)
	}
	res, err := e.svc.Test(context.Background(), e.scope, "inbox-priority", "m1")
	if err != nil || res.Outcome != OutcomeUnclassified || !slices.Equal(res.Replies, want) {
		t.Errorf("Test = %+v, %v; want replies %q", res.Decision, err, want)
	}
}

// Fetch fields that resolve to nothing and fields cut to the budget are
// named on the activity row and by classifier_test; an item none of whose
// fields resolved fails, naming the missing fields, and is never sent to the
// model or acted on.
func TestFetchMissingAndShortenedFields(t *testing.T) {
	e := newEnv(t)
	def := definition()
	obj(def["fetch"])["fields"] = map[string]any{"from": "from.address", "subject": "subject", "body": "body.content", "cc": "cc.address"}
	obj(def["fetch"])["max_chars"] = 50
	e.create(def)
	e.tools.set("mail_read", func(args map[string]any) (string, error) {
		switch args["id"] {
		case "m2":
			return "Mailbox is locked", nil
		case "m3":
			return `{"other":"x"}`, nil
		}
		return `{"from":{"address":"bob@example.com"},"subject":"About m1","body":{"content":"` + strings.Repeat("b", 100) + `"}}`, nil
	})
	rec := e.run("inbox-priority")
	if rec.Acted != 1 || rec.Failed != 2 || e.model.count() != 1 {
		t.Fatalf("run = %+v, model calls %d; want m1 acted, m2 and m3 failed without a model call", rec, e.model.count())
	}
	rows := map[string]ActivityEntry{}
	for _, r := range e.activity(ActivityQuery{}) {
		rows[r.ItemID] = r
	}
	if r := rows["m1"]; !slices.Equal(r.MissingFields, []string{"cc"}) || !slices.Equal(r.ShortenedFields, []string{"body"}) {
		t.Errorf("m1 = missing %q, shortened %q; want [cc], [body]", r.MissingFields, r.ShortenedFields)
	}
	wantM2 := `fetch mail_read did not return JSON (invalid character 'M' looking for beginning of value), so no fetch field resolved ` +
		`(missing: body, cc, from, subject); it returned "Mailbox is locked"`
	if r := rows["m2"]; r.Outcome != OutcomeFailed || r.Error != wantM2 || r.Fields["subject"] != "About m2" {
		t.Errorf("m2 = %+v, want failed with %q and the list entry's fields", r, wantM2)
	}
	if r := rows["m3"]; r.Outcome != OutcomeFailed || r.Error != "fetch mail_read: no fetch field resolved (missing: body, cc, from, subject)" ||
		!slices.Equal(r.MissingFields, []string{"body", "cc", "from", "subject"}) {
		t.Errorf("m3 = %+v", r)
	}
	if moved := e.moves(); len(moved) != 1 || moved["m1"] == nil {
		t.Errorf("moves = %v, want m1 only", moved)
	}
	res, err := e.svc.Test(context.Background(), e.scope, "inbox-priority", "m1")
	if err != nil || !slices.Equal(res.MissingFields, []string{"cc"}) || !slices.Equal(res.ShortenedFields, []string{"body"}) {
		t.Errorf("Test m1 = %+v, %v", res.Decision, err)
	}
	if res, err := e.svc.Test(context.Background(), e.scope, "inbox-priority", "m2"); err != nil || res.Error != wantM2 {
		t.Errorf("Test m2 = %+v, %v", res.Decision, err)
	}
}

// A failed action records the arguments it was called with; a failed fetch
// (and a fetch argument that does not resolve) records the list entry's
// fields, so the item can be recognised.
func TestFailedItemsAreRecognisable(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"m1", "m2"})
	def := definition()
	obj(def["fetch"])["args"] = map[string]any{"id": "{{item.id}}", "folder": "{{item.folder}}"}
	e.create(def)
	// m1's list entry has no folder; m2's fetch works, its action fails.
	e.tools.set("mail_list", func(map[string]any) (string, error) {
		return `{"messages":[{"id":"m1","subject":"About m1"},{"id":"m2","subject":"About m2","folder":"Inbox"}]}`, nil
	})
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "", errors.New(`destination folder "Urgent" not found`) })
	e.run("inbox-priority")
	rows := map[string]ActivityEntry{}
	for _, r := range e.activity(ActivityQuery{}) {
		rows[r.ItemID] = r
	}
	if r := rows["m1"]; r.Error != "fetch.args: {{item.folder}} resolves to nothing for this item" || r.Fields["subject"] != "About m1" || r.Fields["id"] != "m1" {
		t.Errorf("m1 = %+v, want the substitution error and the list entry's fields", r)
	}
	r := rows["m2"]
	if r.Error != `action mail_move failed: destination folder "Urgent" not found` || r.ActionArgs != `{"destination":"low","id":"m2"}` {
		t.Errorf("m2 = %+v, want the action's error and arguments", r)
	}
	if r.Fields["subject"] != "About m2" {
		t.Errorf("m2 fields = %v, want the fetched fields", r.Fields)
	}
}

// Errors are redacted (Host.Redact) before they are stored, logged at warn,
// sent in a notice or summary, or returned by a tool.
func TestErrorsAreRedacted(t *testing.T) {
	const key = "sk-live-0123456789abcdef"
	e := newEnv(t)
	e.svc.host.Redact = func(s string) string { return strings.ReplaceAll(s, key, "[REDACTED]") }
	e.create(definition())
	leak := func(where string, texts ...string) {
		t.Helper()
		for _, s := range texts {
			if strings.Contains(s, key) {
				t.Errorf("%s leaks the key: %q", where, s)
			}
		}
	}
	// An item's model error.
	e.model.setReply(func(string, string) (string, error) {
		return "", fmt.Errorf("model cheap: 429 Too Many Requests (api_key %s)", key)
	})
	e.run("inbox-priority")
	rows := e.activity(ActivityQuery{})
	if len(rows) == 0 || !strings.Contains(rows[0].Error, "[REDACTED]") {
		t.Fatalf("activity = %+v, want the redacted error", rows)
	}
	for _, r := range rows {
		leak("activity", r.Error)
	}
	// A failed run: the stored run, its row, the warn log, the notices.
	e.failingSource("401 Unauthorized: bad key " + key)
	rec := e.run("inbox-priority")
	leak("run", rec.Error)
	if !strings.Contains(rec.Error, "[REDACTED]") {
		t.Errorf("run error = %q", rec.Error)
	}
	st := e.status()
	leak("status", st.LastError, st.LastRun.Error)
	for _, r := range e.runFailures() {
		leak("run_failed", r.Error)
	}
	agent, chat := e.notices()
	leak("notices", append(agent, chat...)...)
	e.log.mu.Lock()
	leak("log", e.log.warns...)
	e.log.mu.Unlock()
	// A tool's refusal.
	res := call(t, Tools(e.svc, fakeToolHost{scope: e.scope}), "test", map[string]any{"id": "inbox-priority"})
	leak("classifier_test", res.ForLLM)
	if !res.IsError || !strings.Contains(res.ForLLM, "[REDACTED]") {
		t.Errorf("classifier_test = %q", res.ForLLM)
	}
}

// A shutdown mid-run (the service's context cancelled) is not a failed run:
// a source call that fails with the cancellation, and an item interrupted
// in flight, record no failure, no run_failed row and no notice; the item
// is taken again by the next run.
func TestShutdownIsNotAFailure(t *testing.T) {
	t.Run("source call", func(t *testing.T) {
		e := newEnv(t)
		e.create(definition())
		ctx, cancel := context.WithCancel(context.Background())
		e.tools.set("mail_list", func(map[string]any) (string, error) {
			cancel()
			return "", context.Canceled
		})
		rec := e.svc.runClassifier(ctx, e.scope, e.def(), nil)
		if rec.Status != RunStopped || rec.Error != "the service stopped" {
			t.Fatalf("run = %+v, want stopped by the service", rec)
		}
		if len(e.runFailures()) != 0 || len(e.notifier.allNotices())+len(e.notifier.all()) != 0 {
			t.Errorf("a shutdown was reported: rows %+v, notices %+v", e.runFailures(), e.notifier.allNotices())
		}
	})
	t.Run("item in flight", func(t *testing.T) {
		e := newEnv(t)
		e.setInbox([]string{"m1"})
		e.create(definition())
		ctx, cancel := context.WithCancel(context.Background())
		e.tools.set("mail_read", func(map[string]any) (string, error) {
			cancel()
			return "", context.Canceled
		})
		rec := e.svc.runClassifier(ctx, e.scope, e.def(), nil)
		if rec.Status != RunStopped || rec.Failed != 0 {
			t.Fatalf("run = %+v, want stopped with nothing failed", rec)
		}
		if rows := e.activity(ActivityQuery{}); len(rows) != 0 {
			t.Errorf("activity = %+v, want nothing recorded", rows)
		}
		if len(e.processed("inbox-priority")) != 0 || e.log.warnings("failed") != 0 {
			t.Errorf("the interrupted item was recorded or logged as failed")
		}
		e.tools.set("mail_read", func(map[string]any) (string, error) { return `{"from":{"address":"bob@example.com"}}`, nil })
		if rec := e.run("inbox-priority"); rec.Acted != 1 {
			t.Errorf("next run = %+v, want m1 taken again", rec)
		}
	})
}

// The advice after the error fits its cause: a tool's own error, a tool the
// agent does not have (the role named), no usable model, the definition.
func TestNoticeAdviceFitsTheCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *env)
		want  string
	}{
		{
			"source error", func(e *env) { e.failingSource("503 Service Unavailable") },
			"Classifier inbox-priority: the run failed: source mail_list failed: 503 Service Unavailable. " +
				"This looks like a problem with mail_list itself (credentials or an outage); check it before changing the classifier.",
		},
		{
			"source missing", func(e *env) { e.tools.remove("mail_list") },
			"Classifier inbox-priority: the run failed: source: tool mail_list is not available to agent alice. " +
				"If mail_list should be available, tell the user; otherwise change the classifier.",
		},
		{
			"action missing", func(e *env) { e.tools.remove("mail_move") },
			"Classifier inbox-priority: the run failed: action for categories low, urgent: tool mail_move is not available to agent alice. " +
				"If mail_move should be available, tell the user; otherwise change the classifier.",
		},
		{"no model", func(e *env) {
			e.model.setReply(func(string, string) (string, error) {
				return "", fmt.Errorf("model cheap is not in Classification (agent Alice): %w", ErrNoModel)
			})
		}, "Classifier inbox-priority: the run failed: model cheap is not in Classification (agent Alice): no usable classifier model. " +
			"Only the operator can fix this on the Task models page; tell the user."},
		{"model error", func(e *env) {
			e.model.setReply(func(string, string) (string, error) { return "", errors.New("model cheap: 500 Internal Server Error") })
		}, "Classifier inbox-priority: the run failed: stopped after 5 consecutive failures; the last: model cheap: 500 Internal Server Error. " +
			"This looks like a problem with model cheap itself (credentials or an outage); check it before changing the classifier."},
		{"definition", func(e *env) {
			e.tools.set("mail_list", func(map[string]any) (string, error) { return `{"items":[]}`, nil })
		}, `Classifier inbox-priority: the run failed: source mail_list returned no list at source.items "messages". ` + failureAdvice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.setInbox([]string{"m1", "m2", "m3", "m4", "m5"})
			e.create(definition())
			tc.setup(e)
			e.runFailed()
			if agent, _ := e.notices(); len(agent) != 1 || agent[0] != tc.want {
				t.Errorf("notice = %q, want %q", agent, tc.want)
			}
			if e.log.warnings("run error:") != 1 {
				t.Errorf("warnings = %q, want the failed run logged at warn", e.log.warns)
			}
		})
	}
}

// A definition file hand-edited to be invalid is reported to its agent as
// a failed run is, once per change: a run_failed row and the notices,
// counted and muted alike. A file whose owner cannot be read is the
// folder's agent's when it is the only one.
func TestInvalidDefinitionFileIsReported(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	scan := func() {
		e.svc.scanBase(context.Background(), e.scope.BaseDirectory, []Scope{e.scope}, map[string]bool{})
	}
	scan()
	path := definitionPath(e.scope.BaseDirectory, "inbox-priority")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edit := func(data string) {
		t.Helper()
		e.clock.Advance(time.Minute)
		if err := os.WriteFile(path, []byte(data), 0o640); err != nil {
			t.Fatal(err)
		}
		mt := e.clock.Now()
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	edit(strings.Replace(string(raw), `"enabled"`, `"bogus": 1, "enabled"`, 1))
	scan()
	scan() // unchanged: not reported again
	const first = `Classifier inbox-priority is not run: the definition is not valid: unknown field "bogus".`
	const advice = " Fix it with classifier_update, or delete it and create it again."
	e.expectNotices("first", []string{first + advice}, []string{first})
	if rows := e.runFailures(); len(rows) != 1 || rows[0].Error != `the definition is not valid: unknown field "bogus".` {
		t.Errorf("run_failed rows = %+v", rows)
	}

	edit(`{"id": "inbox-priority", "owner": `) // broken JSON: the owner cannot be read
	scan()
	agent, chat := e.notices()
	if len(agent) != 2 || !strings.HasPrefix(agent[1], "Classifier inbox-priority is not run: the definition is not valid") ||
		!strings.HasSuffix(agent[1], muteNotice) || len(chat) != 2 || !strings.HasSuffix(chat[1], muteSummary) {
		t.Errorf("second notice = %q / %q, want the muted one", agent, chat)
	}
	edit(`{}`)
	scan()
	if agent, _ := e.notices(); len(agent) != 2 {
		t.Errorf("a muted change was reported: %q", agent)
	}
}

// A state.db that cannot be opened is logged at error, raises the operator
// alert and is reported to the agent and its chat once per ErrorMute, with
// advice for the operator; it opens again silently. Check Up's CheckState
// finds it.
func TestStateCannotBeOpened(t *testing.T) {
	e := newEnv(t)
	e.log.expectErrors()
	e.create(definition())
	def := e.def()
	if err := e.svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(e.scope.BaseDirectory, "state.db")
	for _, side := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(statePath + side); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	e.svc = New(e.svc.host)
	if err := CheckState(context.Background(), e.scope.BaseDirectory); err == nil || !strings.Contains(err.Error(), "is not a file") {
		t.Errorf("CheckState = %v, want not a file", err)
	}

	run := func() RunRecord { return e.svc.runClassifier(context.Background(), e.scope, def, nil) }
	rec := run()
	if rec.Status != RunError || rec.Error != "the classifier's state could not be opened" {
		t.Fatalf("run = %+v", rec)
	}
	const summary = "Classifier inbox-priority: the run failed: the classifier's state could not be opened."
	const notice = summary + " Only the operator can fix this; tell the user."
	e.expectNotices("first", []string{notice}, []string{summary})
	alerts := e.notifier.allAlerts()
	if len(alerts) != 1 || alerts[0].agent != "alice" || alerts[0].classifier != "inbox-priority" || alerts[0].path != statePath || alerts[0].err == nil {
		t.Errorf("alerts = %+v", alerts)
	}
	if errs := e.log.errors(); len(errs) != 1 || !strings.Contains(errs[0], "open state") {
		t.Errorf("error log = %q", errs)
	}
	run()
	e.clock.Advance(ErrorMute - time.Minute)
	run()
	e.expectNotices("muted", []string{notice}, []string{summary})
	if len(e.notifier.allAlerts()) != 1 {
		t.Errorf("alerts = %d, want 1 while muted", len(e.notifier.allAlerts()))
	}
	e.clock.Advance(time.Minute)
	run()
	e.expectNotices("after the mute", []string{notice, notice}, []string{summary, summary})
	if len(e.notifier.allAlerts()) != 2 {
		t.Errorf("alerts = %d, want 2 after the mute", len(e.notifier.allAlerts()))
	}

	// The tools say so, without a path.
	res := call(t, Tools(e.svc, fakeToolHost{scope: e.scope}), "status", map[string]any{"id": "inbox-priority"})
	if want := "The classifier's state could not be opened. Only the operator can fix this; tell the user."; !res.IsError || res.ForLLM != want {
		t.Errorf("status = %q, want %q", res.ForLLM, want)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := CheckState(context.Background(), e.scope.BaseDirectory); err != nil {
		t.Errorf("CheckState with no state.db = %v", err)
	}
	if rec := run(); rec.Status != RunOK {
		t.Errorf("run after the fix = %+v", rec)
	}
}
