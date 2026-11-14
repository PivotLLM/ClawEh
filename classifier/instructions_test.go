// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeInstructions is the host's Markdown reader over maps of path to
// content; a path in neither map does not exist, one in errs fails.
type fakeInstructions struct {
	mu     sync.Mutex
	files  map[string]string
	dirs   map[string][]NamedText
	errs   map[string]error
	agents []string // the agent of every read
}

func (f *fakeInstructions) ReadInstructionsFile(agentID, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents = append(f.agents, agentID)
	if err := f.errs[path]; err != nil {
		return "", err
	}
	text, ok := f.files[path]
	if !ok {
		return "", fmt.Errorf("it does not exist: %w", fs.ErrNotExist)
	}
	return text, nil
}

func (f *fakeInstructions) ReadInstructionsDir(agentID, path string) ([]NamedText, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents = append(f.agents, agentID)
	if err := f.errs[path]; err != nil {
		return nil, err
	}
	list, ok := f.dirs[path]
	if !ok {
		return nil, fmt.Errorf("it does not exist: %w", fs.ErrNotExist)
	}
	return slices.Clone(list), nil
}

func (f *fakeInstructions) setFile(path, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = text
}

func (f *fakeInstructions) setDir(path string, list ...NamedText) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[path] = list
}

func (f *fakeInstructions) setErr(path string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errs == nil {
		f.errs = map[string]error{}
	}
	f.errs[path] = err
}

// markdownDefinition is the inbox classifier with its guidance in
// files/classifiers/inbox/guide.md and its rules in .../rules, and no
// inline instructions or rules.
func markdownDefinition() map[string]any {
	m := definition()
	c := obj(m["classify"])
	c["instructions"] = ""
	delete(c, "rules")
	c["instructions_file"] = "files/classifiers/inbox/guide.md"
	c["instructions_dir"] = "files/classifiers/inbox/rules"
	return m
}

func TestValidInstructionsPath(t *testing.T) {
	for p, want := range map[string]bool{
		"files/a.md":                  true,
		"files/classifiers/inbox/x":   true,
		"files":                       true,
		"./files/a.md":                true,
		"files/x/../a.md":             true,
		"":                            false,
		"/files/a.md":                 false,
		"/home/alice/ws/files/a.md":   false,
		"../files/a.md":               false,
		"files/../sessions/a.md":      false,
		"files/../../a.md":            false,
		"classifiers/a.md":            false,
		"filesx/a.md":                 false,
		`files\a.md`:                  false,
		"skills/classifier/README.md": false,
	} {
		if got := ValidInstructionsPath(p); got != want {
			t.Errorf("ValidInstructionsPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestValidateStaticInstructionsPaths(t *testing.T) {
	ok := []func(c map[string]any){
		func(c map[string]any) { c["instructions_file"] = "files/guide.md" },
		func(c map[string]any) { c["instructions_dir"] = "files/rules" },
		func(c map[string]any) {
			c["instructions_file"] = "files/guide.MD"
			c["instructions_dir"] = "files/rules"
		},
		// Empty inline instructions are fine once a file or folder is set.
		func(c map[string]any) { c["instructions"] = ""; c["instructions_dir"] = "files/rules" },
		func(c map[string]any) { delete(c, "instructions"); c["instructions_file"] = "files/guide.md" },
	}
	for i, change := range ok {
		m := definition()
		change(obj(m["classify"]))
		if err := ValidateStatic(decodeMap(t, m), testCeilings); err != nil {
			t.Errorf("case %d refused: %v", i, err)
		}
	}
	for _, tc := range []struct {
		field, value, want string
	}{
		{"instructions_file", "/srv/guide.md", `classify.instructions_file "/srv/guide.md" must be a path inside files/`},
		{"instructions_file", "../guide.md", `classify.instructions_file "../guide.md" must be a path inside files/`},
		{"instructions_file", "files/../memory/guide.md", `classify.instructions_file "files/../memory/guide.md" must be a path inside files/`},
		{"instructions_file", "notes/guide.md", `classify.instructions_file "notes/guide.md" must be a path inside files/`},
		{"instructions_file", "files/guide.txt", `classify.instructions_file "files/guide.txt" must name a .md file.`},
		{"instructions_dir", "/srv/rules", `classify.instructions_dir "/srv/rules" must be a path inside files/`},
		{"instructions_dir", "files/../../rules", `classify.instructions_dir "files/../../rules" must be a path inside files/`},
	} {
		m := definition()
		obj(m["classify"])[tc.field] = tc.value
		err := ValidateStatic(decodeMap(t, m), testCeilings)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %q: %v, want %q", tc.field, tc.value, err, tc.want)
		}
	}
}

// Files load alphabetically by name ignoring case, numbers as text, ties by
// the exact name; names without numbers load as well.
func TestInstructionsOrder(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{
			in:   []string{"33-yyy.md", "00-xxx.md", "B-rule.md", "a-rule.md", "10-z.md", "2-z.md"},
			want: []string{"00-xxx.md", "10-z.md", "2-z.md", "33-yyy.md", "a-rule.md", "B-rule.md"},
		},
		{
			in:   []string{"example.org.md", "Example.com.md", "billing.md", "example.com.md"},
			want: []string{"billing.md", "Example.com.md", "example.com.md", "example.org.md"},
		},
	} {
		list := make([]NamedText, len(tc.in))
		for i, n := range tc.in {
			list[i] = NamedText{Name: n}
		}
		instructionsOrder(list)
		got := make([]string, len(list))
		for i, f := range list {
			got[i] = f.Name
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("order of %v = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The system prompt holds the inline instructions, the file, each folder
// file under its name, then the numbered rules and the categories.
func TestBuildPromptMarkdownOrder(t *testing.T) {
	def := decodeMap(t, definition())
	in := instructions{
		file: "\nGUIDE: decide how important it is.\n",
		dir:  []NamedText{{Name: "00-bob.md", Text: "BOB rule."}, {Name: "billing.md", Text: "BILLING rule.\n"}},
	}
	def.Classify.InstructionsFile = "files/classifiers/inbox/guide.md"
	system, _ := buildPrompt(def, in, itemData{}, "", "T0K")
	want := "## instructions\n\nDecide how important this email is to the user.\n\n" +
		"## guide.md\n\nGUIDE: decide how important it is.\n\n" +
		"## 00-bob.md\n\nBOB rule.\n\n" +
		"## billing.md\n\nBILLING rule.\n\n" +
		"Rules:\n1. Email from bob@example.com is from a client and is always urgent.\n\n" +
		"Categories:\n"
	if !strings.HasPrefix(system, want) {
		t.Errorf("system prompt:\n%s\nwant it to start with:\n%s", system, want)
	}

	// Without inline instructions the Markdown starts the prompt.
	def.Classify.Instructions = ""
	system, _ = buildPrompt(def, instructions{dir: in.dir}, itemData{}, "", "T0K")
	if !strings.HasPrefix(system, "## 00-bob.md\n\nBOB rule.\n\n## billing.md") {
		t.Errorf("system prompt without inline instructions:\n%s", system)
	}
}

// A run reads the Markdown, in name order, and sends it to the model; an
// edit is used by the next run without a definition change.
func TestRunReadsMarkdownEveryRun(t *testing.T) {
	e := newEnv(t)
	e.instr.setFile("files/classifiers/inbox/guide.md", "GUIDE v1")
	e.instr.setDir("files/classifiers/inbox/rules",
		NamedText{Name: "b-newsletters.md", Text: "NEWSLETTER rule"},
		NamedText{Name: "a-example.com.md", Text: "EXAMPLE rule"})
	e.create(markdownDefinition())
	e.setInbox([]string{"m1"})
	if rec := e.run("inbox-priority"); rec.Status != RunOK || rec.Classified != 1 {
		t.Fatalf("first run = %+v", rec)
	}
	sys := e.model.systems[0]
	if !strings.Contains(sys, "GUIDE v1\n\n## a-example.com.md\n\nEXAMPLE rule\n\n## b-newsletters.md\n\nNEWSLETTER rule\n") {
		t.Errorf("system prompt:\n%s", sys)
	}

	e.instr.setFile("files/classifiers/inbox/guide.md", "GUIDE v2")
	e.setInbox([]string{"m1", "m2"})
	if rec := e.run("inbox-priority"); rec.Status != RunOK || rec.Classified != 1 {
		t.Fatalf("second run = %+v", rec)
	}
	if sys := e.model.systems[len(e.model.systems)-1]; !strings.Contains(sys, "GUIDE v2") || strings.Contains(sys, "GUIDE v1") {
		t.Errorf("the edit was not used by the next run:\n%s", sys)
	}
	if got := e.model.count(); got != 2 {
		t.Errorf("model calls = %d, want 2 (m1 is not reprocessed)", got)
	}
}

// A missing or unreadable file or folder, a prompt over max_prompt_chars
// with the Markdown, and no instructions at all each fail the run before the
// source is called: nothing is classified, and the run's error names the
// path (not the classifier, which the summary names once).
func TestRunMarkdownProblems(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *env)
		want  string
	}{
		{"missing file", func(e *env) {
			e.instr.setDir("files/classifiers/inbox/rules")
		}, "classify.instructions_file files/classifiers/inbox/guide.md can't be read: it does not exist"},
		{"missing folder", func(e *env) {
			e.instr.setFile("files/classifiers/inbox/guide.md", "GUIDE")
		}, "classify.instructions_dir files/classifiers/inbox/rules can't be read: it does not exist"},
		{"refused file", func(e *env) {
			e.instr.setDir("files/classifiers/inbox/rules")
			e.instr.setErr("files/classifiers/inbox/guide.md", errors.New("it leads outside files/"))
		}, "classify.instructions_file files/classifiers/inbox/guide.md can't be read: it leads outside files/"},
		{"over the limit", func(e *env) {
			e.instr.setFile("files/classifiers/inbox/guide.md", strings.Repeat("g", 150))
			e.instr.setDir("files/classifiers/inbox/rules", NamedText{Name: "a.md", Text: strings.Repeat("r", 100)})
			e.mu.Lock()
			e.ceilings.MaxPromptChars = 200
			e.mu.Unlock()
		}, "classify.instructions, classify.rules and the Markdown files have 250 characters together; this install allows 200"},
		{"no instructions", func(e *env) {
			e.instr.setFile("files/classifiers/inbox/guide.md", " \n")
			e.instr.setDir("files/classifiers/inbox/rules")
		}, "classify.instructions and its Markdown files are all empty, so there are no instructions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			m := markdownDefinition()
			m["report"] = map[string]any{"summary": "always"}
			e.create(m) // nothing exists yet: accepted
			tc.setup(e)
			listed := len(e.tools.callsOf("mail_list"))
			rec := e.run("inbox-priority")
			if rec.Status != RunError || !strings.Contains(rec.Error, tc.want) {
				t.Fatalf("run = %+v, want an error containing %q", rec, tc.want)
			}
			if rec.Classified+rec.NewItems+e.model.count() != 0 || len(e.tools.callsOf("mail_list")) != listed {
				t.Errorf("the run classified or listed: %+v, %d model calls", rec, e.model.count())
			}
			if len(e.processed("inbox-priority")) != 0 {
				t.Error("items were recorded as processed")
			}
			st, err := e.svc.Status(e.scope, "inbox-priority")
			if err != nil || len(st) != 1 || !strings.Contains(st[0].LastError, tc.want) {
				t.Errorf("status = %+v, %v", st, err)
			}
			sums, err := Inspect(context.Background(), e.scope.BaseDirectory, e.ceilings)
			if err != nil || len(sums) != 1 || sums[0].LastRun == nil || sums[0].LastRun.Status != RunError ||
				!strings.Contains(sums[0].LastRun.Error, tc.want) {
				t.Errorf("Inspect = %+v, %v", sums, err)
			}
			// The summary names the classifier once, before the error.
			want := "Classifier inbox-priority: the run failed (" + tc.want
			if texts := e.notifier.all(); len(texts) != 1 || !strings.HasPrefix(texts[0], want) {
				t.Errorf("summaries = %q, want one starting %q", texts, want)
			}
			// classifier_test refuses with the same sentence.
			if _, err := e.svc.Test(context.Background(), e.scope, "inbox-priority", ""); err == nil ||
				!strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Errorf("Test = %v", err)
			}
		})
	}
}

// Create and update accept Markdown that does not exist yet, and refuse
// Markdown that exists but can't be read or is over max_prompt_chars.
func TestCreateChecksExistingMarkdown(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Create(context.Background(), e.scope, mustJSON(t, markdownDefinition())); err != nil {
		t.Fatalf("Create with missing Markdown: %v", err)
	}
	e.mu.Lock()
	e.ceilings.MaxPromptChars = 100
	e.mu.Unlock()
	e.instr.setDir("files/classifiers/inbox/rules", NamedText{Name: "a.md", Text: strings.Repeat("r", 101)})
	err := e.svc.Update(context.Background(), e.scope, "inbox-priority", []byte(`{"enabled":false}`))
	if want := "classify.instructions, classify.rules and the Markdown files have 101 characters together; this install allows 100."; err == nil || err.Error() != want {
		t.Errorf("Update over the limit = %v, want %q", err, want)
	}
	e.instr.setDir("files/classifiers/inbox/rules")
	e.instr.setErr("files/classifiers/inbox/guide.md", errors.New("it is a folder"))
	m := markdownDefinition()
	m["id"] = "other"
	_, err = e.svc.Create(context.Background(), e.scope, mustJSON(t, m))
	if want := "classify.instructions_file files/classifiers/inbox/guide.md can't be read: it is a folder."; err == nil || err.Error() != want {
		t.Errorf("Create with an unreadable file = %v, want %q", err, want)
	}
	if _, ok := errors.AsType[*ValidationError](err); !ok {
		t.Errorf("error %T is not a ValidationError", err)
	}
}

// classifier_test shows the system prompt with the Markdown loaded; a
// clone's call reads as the agent it copies.
func TestTestShowsLoadedMarkdown(t *testing.T) {
	e := newEnv(t)
	e.instr.setFile("files/classifiers/inbox/guide.md", "GUIDE text")
	e.instr.setDir("files/classifiers/inbox/rules", NamedText{Name: "00-bob.md", Text: "Mail from bob@example.com is urgent."})
	e.create(markdownDefinition())
	clone := e.scope
	clone.Clone = "1a2b3c4d"
	res, err := e.svc.Test(context.Background(), clone, "inbox-priority", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Category != "urgent" || !strings.Contains(res.SystemPrompt, "GUIDE text\n\n## 00-bob.md\n\nMail from bob@example.com is urgent.") ||
		!strings.Contains(res.SystemPrompt, "Categories:\n- urgent") {
		t.Errorf("Test = %+v", res)
	}
	e.instr.mu.Lock()
	agents := slices.Clone(e.instr.agents)
	e.instr.mu.Unlock()
	if len(agents) == 0 || slices.ContainsFunc(agents, func(a string) bool { return a != "alice" }) {
		t.Errorf("Markdown read as %v, want alice only", agents)
	}
}

// Without a host reader a classifier naming Markdown fails its runs.
func TestRunWithoutInstructionsReader(t *testing.T) {
	e := newEnv(t)
	e.instr.setFile("files/classifiers/inbox/guide.md", "GUIDE")
	e.instr.setDir("files/classifiers/inbox/rules")
	e.create(markdownDefinition())
	e.svc.host.Instructions = nil
	if rec := e.run("inbox-priority"); rec.Status != RunError || !strings.Contains(rec.Error, "this install can't read Markdown instructions") {
		t.Errorf("run = %+v", rec)
	}
}
