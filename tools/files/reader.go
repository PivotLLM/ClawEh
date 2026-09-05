package files

import (
	"io"
	"io/fs"
	"os"
	"regexp"

	"github.com/PivotLLM/ClawEh/config"
)

// ReadPolicy derives the agent read policy from config: whether reads are
// confined to the workspace, and the host paths that bypass that confinement
// (Tools.AllowReadPaths plus the global skills directory). It is the single
// definition used by both the file tools (RegisterTools) and Reader, so anything
// reading on an agent's behalf shares one policy.
func ReadPolicy(c *config.Config) (restrict bool, allow []*regexp.Regexp) {
	if c == nil {
		return false, nil
	}
	restrict = c.Agents.Defaults.RestrictToWorkspace && !c.Agents.Defaults.AllowReadOutsideWorkspace
	allow = compilePatterns(c.Tools.AllowReadPaths)
	// Always allow reading from the global skills directory.
	if skillsPath := c.SkillsPath(); skillsPath != "" {
		if re, err := regexp.Compile("^" + regexp.QuoteMeta(skillsPath) + "/"); err == nil {
			allow = append(allow, re)
		}
	}
	return restrict, allow
}

// Reader reads files on an agent's behalf through exactly the fileSystem stack
// the file tools use: workspace sandbox → allow-list patterns → read-scope
// subdirs (files/, skills/, tasks/, tmp/) → external mounts (maestro/, ...).
// A path a Reader can open is a path file_read would open, and nothing more —
// the permission check is the same code, not a reimplementation of it.
//
// Non-tool consumers (cognitive-memory file attachments) use this so a stored
// path cannot reach outside what the agent is allowed to read.
type Reader struct {
	workspace string
	restrict  bool
	allow     []*regexp.Regexp
}

// NewReader builds a Reader for an agent's workspace using the config read policy.
func NewReader(c *config.Config, workspace string) *Reader {
	restrict, allow := ReadPolicy(c)
	return &Reader{workspace: workspace, restrict: restrict, allow: allow}
}

// fs builds the layered filesystem for one operation. It is rebuilt per call
// rather than cached because the read-scope and per-workspace mounts are
// installed when the file tools are registered, which may happen after a Reader
// is constructed; building late means a Reader always sees current policy.
func (r *Reader) fs() fileSystem {
	return buildFs(r.workspace, r.restrict, r.allow)
}

// ReadFile reads path (workspace-relative, or a mount path like "maestro/x.md"),
// returning an access-denied error when the agent is not permitted to read it.
func (r *Reader) ReadFile(path string) ([]byte, error) { return r.fs().ReadFile(path) }

// Stat stats path under the same permission rules as ReadFile. Used to size a
// file (and read its mtime) before deciding to read it.
func (r *Reader) Stat(path string) (os.FileInfo, error) { return r.fs().Stat(path) }

// Open opens path for reading under the same permission rules as ReadFile.
func (r *Reader) Open(path string) (fs.File, error) { return r.fs().Open(path) }

// ReadFileLimit reads at most limit bytes of path and reports whether the file
// held more. Unlike ReadFile it never materializes the whole file, so an
// unexpectedly huge file costs one bounded read rather than its full size in
// memory. A limit <= 0 means unlimited (equivalent to ReadFile).
func (r *Reader) ReadFileLimit(path string, limit int) (data []byte, more bool, err error) {
	if limit <= 0 {
		data, err = r.ReadFile(path)
		return data, false, err
	}
	f, err := r.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	// Read one byte past the limit: its presence is what proves the file is
	// longer than the cap, without reading the remainder.
	data, err = io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}
