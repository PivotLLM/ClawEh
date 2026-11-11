// ClawEh
// License: MIT

package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/tools/files"
	toolsmsg "github.com/PivotLLM/ClawEh/tools/msg"
	"github.com/PivotLLM/ClawEh/utils"
)

var _ classifier.InstructionsReader = (*ClassifierHost)(nil)

// errInstructionsMissing is a Markdown file or folder that does not exist.
var errInstructionsMissing error = instructionsMissingError{}

// instructionsMissingError reads "it does not exist" and is fs.ErrNotExist.
type instructionsMissingError struct{}

func (instructionsMissingError) Error() string        { return "it does not exist" }
func (instructionsMissingError) Is(target error) bool { return target == fs.ErrNotExist }

// instructionsReader is what reading one agent's classifier Markdown needs:
// the agent's file-read permission (files.Reader, the file tools' own
// check), its workspace and its files/ folder with links resolved.
type instructionsReader struct {
	reader    *files.Reader
	workspace string
	filesRoot string
}

// instructionsReaderFor returns the reader of agentID's classifier
// Markdown: only for an agent granted a file-reading tool (as
// msg_send_file), through files.Reader with the loop's current
// configuration.
func (h *ClassifierHost) instructionsReaderFor(agentID string) (*instructionsReader, error) {
	al, cfg, err := h.bound()
	if err != nil {
		return nil, err
	}
	r := al.GetRegistry()
	if r == nil {
		return nil, errLoopNotReady
	}
	a := cfg.AgentByID(agentID)
	inst, ok := r.GetConfigured(agentID)
	if a == nil || !ok || inst == nil || inst.Workspace == "" {
		return nil, fmt.Errorf("agent %s is not running", agentID)
	}
	if !toolsmsg.CanReadFiles(a) {
		return nil, fmt.Errorf("agent %s has no file-reading tool (%s)", a.DisplayName(), strings.Join(toolsmsg.ReadTools, " or "))
	}
	ws, err := filepath.Abs(inst.Workspace)
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(filepath.Join(ws, classifier.FilesDirName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errInstructionsMissing
		}
		return nil, errors.New("the agent's files folder can't be read")
	}
	return &instructionsReader{reader: files.NewReader(cfg, ws), workspace: ws, filesRoot: root}, nil
}

// ReadInstructionsFile implements classifier.InstructionsReader.
func (h *ClassifierHost) ReadInstructionsFile(agentID, rel string) (string, error) {
	ir, err := h.instructionsReaderFor(agentID)
	if err != nil {
		return "", err
	}
	data, err := ir.readFile(rel, classifier.MaxInstructionsFileBytes)
	return string(data), err
}

// ReadInstructionsDir implements classifier.InstructionsReader: every
// non-hidden entry directly in the folder whose name ends in .md (any case)
// and that is, its link followed, a regular file. A link leading out of
// files/ or to what the agent may not read is refused, not skipped.
func (h *ClassifierHost) ReadInstructionsDir(agentID, rel string) ([]classifier.NamedText, error) {
	ir, err := h.instructionsReaderFor(agentID)
	if err != nil {
		return nil, err
	}
	target, err := ir.check(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, errors.New("the agent may not read it")
	}
	if !info.IsDir() {
		return nil, errors.New("it is not a folder")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, errors.New("its files can't be listed")
	}
	var out []classifier.NamedText
	total := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		switch t := e.Type(); {
		case t.IsRegular():
		case t&fs.ModeSymlink != 0:
			fi, statErr := os.Stat(filepath.Join(target, name))
			if statErr == nil && !fi.Mode().IsRegular() {
				continue // a link to a folder or a device
			}
		default:
			continue
		}
		data, err := ir.readFile(path.Join(rel, name), classifier.MaxInstructionsFileBytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if total += len(data); total > classifier.MaxInstructionsDirBytes {
			return nil, fmt.Errorf("its .md files have more than %d bytes together", classifier.MaxInstructionsDirBytes)
		}
		out = append(out, classifier.NamedText{Name: name, Text: string(data)})
	}
	return out, nil
}

// check resolves rel (workspace-relative) as the agent's file tools would
// read it and returns its link-resolved target, which must lie in the
// agent's files/ folder.
func (ir *instructionsReader) check(rel string) (string, error) {
	if _, err := os.Stat(filepath.Join(ir.workspace, filepath.FromSlash(rel))); errors.Is(err, fs.ErrNotExist) {
		return "", errInstructionsMissing
	}
	abs, err := ir.reader.Resolve(rel)
	if err != nil {
		return "", errors.New("the agent may not read it")
	}
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", errors.New("the agent may not read it")
	}
	if r, relErr := filepath.Rel(ir.filesRoot, target); relErr != nil || (r != "." && !filepath.IsLocal(r)) {
		return "", errors.New("it leads outside files/")
	}
	if ir.reader.Allowed(target) != nil {
		return "", errors.New("the agent may not read it")
	}
	return target, nil
}

// readFile reads the regular file rel through the agent's file sandbox:
// checked (check), opened with files.Reader.Open, compared with what was
// checked, and at most limit bytes.
func (ir *instructionsReader) readFile(rel string, limit int) ([]byte, error) {
	target, err := ir.check(rel)
	if err != nil {
		return nil, err
	}
	checked, err := os.Lstat(target)
	switch {
	case err != nil:
		return nil, errors.New("the agent may not read it")
	case checked.IsDir():
		return nil, errors.New("it is a folder")
	case !checked.Mode().IsRegular():
		return nil, errors.New("it is not a regular file")
	case checked.Size() > int64(limit):
		return nil, fmt.Errorf("it has more than %d bytes", limit)
	}
	f, err := ir.reader.Open(target)
	if err != nil {
		return nil, errors.New("the agent may not read it")
	}
	defer utils.CloseQuietly(f)
	if info, statErr := f.Stat(); statErr != nil || !os.SameFile(checked, info) {
		return nil, errors.New("it changed while it was being read")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, errors.New("it can't be read")
	}
	if len(data) > limit {
		return nil, fmt.Errorf("it has more than %d bytes", limit)
	}
	return data, nil
}
