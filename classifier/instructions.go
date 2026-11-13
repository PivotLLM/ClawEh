// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// FilesDirName is the workspace folder a classifier's Markdown instructions
// must lie in.
const FilesDirName = "files"

// errNoInstructionsReader is a run's failure when the host reads no
// Markdown instructions.
var errNoInstructionsReader = errors.New("this install can't read Markdown instructions")

// ValidInstructionsPath reports whether p may be a classify.instructions_file
// or instructions_dir: a relative path, with no .. leading out, inside the
// workspace's files/ folder (files itself included).
func ValidInstructionsPath(p string) bool {
	if p == "" || strings.Contains(p, `\`) || filepath.IsAbs(p) || !filepath.IsLocal(p) {
		return false
	}
	clean := path.Clean(p)
	return clean == FilesDirName || strings.HasPrefix(clean, FilesDirName+"/")
}

// isMarkdownName reports whether a file name ends in .md, in any case.
func isMarkdownName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".md")
}

// checkInstructionsPaths returns the issues of classify.instructions_file
// and instructions_dir.
func checkInstructionsPaths(c Classify) []string {
	var issues []string
	if f := c.InstructionsFile; f != "" {
		switch {
		case !ValidInstructionsPath(f):
			issues = append(issues, fmt.Sprintf("classify.instructions_file %q must be a path inside files/, such as files/classifiers/inbox/guide.md.", f))
		case !isMarkdownName(f):
			issues = append(issues, fmt.Sprintf("classify.instructions_file %q must name a .md file.", f))
		}
	}
	if d := c.InstructionsDir; d != "" && !ValidInstructionsPath(d) {
		issues = append(issues, fmt.Sprintf("classify.instructions_dir %q must be a path inside files/, such as files/classifiers/inbox/rules.", d))
	}
	return issues
}

// instructions is the Markdown a classifier read for one run (or one test):
// the file's content and the folder's files in load order.
type instructions struct {
	file string
	dir  []NamedText
}

// instructionsOrder sorts a folder's files alphabetically by name, ignoring
// case, ties broken by the exact name, so the order is the same everywhere.
// Numbers compare as text: 10-x comes before 2-x.
func instructionsOrder(list []NamedText) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if a != b {
			return a < b
		}
		return list[i].Name < list[j].Name
	})
}

// promptChars is the characters of the definition's instructions and rules
// together with the Markdown read (in), as max_prompt_chars counts them.
func promptChars(def *Definition, in instructions) int {
	n := utf8.RuneCountInString(def.Classify.Instructions) + utf8.RuneCountInString(in.file)
	for _, f := range in.dir {
		n += utf8.RuneCountInString(f.Text)
	}
	for _, r := range def.Classify.Rules {
		n += utf8.RuneCountInString(r)
	}
	return n
}

// instructionsError is a Markdown file or folder that could not be read.
type instructionsError struct {
	field, path string
	err         error
}

func (e *instructionsError) Error() string {
	return fmt.Sprintf("classify.%s %s can't be read: %v", e.field, e.path, e.err)
}

func (e *instructionsError) Unwrap() error { return e.err }

// readInstructions reads the definition's Markdown as scope's agent. With
// missingOK, a file or folder that does not exist is left out (a create or
// update: it may be written later); otherwise it is an *instructionsError.
func (s *Service) readInstructions(scope Scope, def *Definition, missingOK bool) (instructions, error) {
	var in instructions
	c := def.Classify
	if c.InstructionsFile == "" && c.InstructionsDir == "" {
		return in, nil
	}
	r := s.host.Instructions
	if r == nil {
		return in, errNoInstructionsReader
	}
	if c.InstructionsFile != "" {
		text, err := r.ReadInstructionsFile(scope.AgentID, path.Clean(c.InstructionsFile))
		switch {
		case err == nil:
			in.file = strings.ToValidUTF8(text, "�")
		case missingOK && errors.Is(err, fs.ErrNotExist):
		default:
			return in, &instructionsError{field: "instructions_file", path: c.InstructionsFile, err: err}
		}
	}
	if c.InstructionsDir != "" {
		list, err := r.ReadInstructionsDir(scope.AgentID, path.Clean(c.InstructionsDir))
		switch {
		case err == nil:
			for i := range list {
				list[i].Text = strings.ToValidUTF8(list[i].Text, "�")
			}
			instructionsOrder(list)
			in.dir = list
		case missingOK && errors.Is(err, fs.ErrNotExist):
		default:
			return in, &instructionsError{field: "instructions_dir", path: c.InstructionsDir, err: err}
		}
	}
	return in, nil
}

// loadInstructions reads the definition's Markdown for a run or a test and
// checks the prompt against the install's max_prompt_chars. Its error is
// one sentence, without a final period (a run's error); it does not name
// the classifier, which every place showing it names already.
func (s *Service) loadInstructions(scope Scope, def *Definition) (instructions, error) {
	in, err := s.readInstructions(scope, def, false)
	if err != nil {
		return in, err
	}
	if def.Classify.InstructionsFile == "" && def.Classify.InstructionsDir == "" {
		return in, nil // ValidateStatic counted it all
	}
	if limit := s.host.ceilings().MaxPromptChars; limit > 0 {
		if n := promptChars(def, in); n > limit {
			return in, fmt.Errorf("classify.instructions, classify.rules and the Markdown files have %d characters together; this install allows %d", n, limit)
		}
	}
	if strings.TrimSpace(def.Classify.Instructions) == "" && strings.TrimSpace(in.file) == "" && !slicesAnyText(in.dir) {
		return in, errors.New("classify.instructions and its Markdown files are all empty, so there are no instructions")
	}
	return in, nil
}

// checkInstructions is the create, update and validate check of the
// Markdown that exists now: readable, and within max_prompt_chars together
// with the instructions and rules.
func (s *Service) checkInstructions(scope Scope, def *Definition) error {
	if def.Classify.InstructionsFile == "" && def.Classify.InstructionsDir == "" {
		return nil
	}
	in, err := s.readInstructions(scope, def, true)
	if err != nil {
		return &ValidationError{Issues: []string{err.Error() + "."}}
	}
	if limit := s.host.ceilings().MaxPromptChars; limit > 0 {
		if n := promptChars(def, in); n > limit {
			return &ValidationError{Issues: []string{fmt.Sprintf("classify.instructions, classify.rules and the Markdown files have %d characters together; this install allows %d.", n, limit)}}
		}
	}
	return nil
}

func slicesAnyText(list []NamedText) bool {
	for _, f := range list {
		if strings.TrimSpace(f.Text) != "" {
			return true
		}
	}
	return false
}
