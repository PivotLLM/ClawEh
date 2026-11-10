package files

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/utils"
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
// subdirs (the configured ones plus the always-readable tasks/, tmp/ and
// forums/) → allowed folders (and maestro/ with Maestro on).
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
// rather than cached because the read-scope and per-workspace allowed folders are
// installed when the file tools are registered, which may happen after a Reader
// is constructed; building late means a Reader always sees current policy.
func (r *Reader) fs() fileSystem {
	return buildFs(r.workspace, r.restrict, r.allow)
}

// ReadFile reads path (workspace-relative, or absolute in an allowed folder),
// returning an access-denied error when the agent is not permitted to read it.
func (r *Reader) ReadFile(path string) ([]byte, error) { return r.fs().ReadFile(path) }

// Stat stats path under the same permission rules as ReadFile. Used to size a
// file (and read its mtime) before deciding to read it.
func (r *Reader) Stat(path string) (os.FileInfo, error) { return r.fs().Stat(path) }

// Open opens path for reading through the same file-system stack that
// checks it, and returns only a regular file (a folder, FIFO or device is
// refused; the open never waits). What protects the open depends on where
// path leads:
//   - the workspace (a confined agent) or an allowed folder: the open is
//     made inside that place's os.Root, so a link swapped in after an
//     earlier check (Resolve, Allowed, Stat) cannot lead it outside; a link
//     inside the root is still followed;
//   - a host path (an agent not confined to its workspace, an allow-listed
//     path, the skills directory): the links are resolved first, the target
//     must be a regular file, and it is opened with O_NOFOLLOW and compared
//     with what was checked, so a link swapped in at the last moment is
//     refused.
//
// A caller that checked a file itself compares the open file with it
// (os.SameFile). An absolute path inside the workspace is taken as a
// workspace path, as Allowed takes it. The caller closes the file.
func (r *Reader) Open(path string) (*os.File, error) {
	if filepath.IsAbs(path) {
		path = r.virtualPath(filepath.Clean(path))
	}
	f, err := r.fs().Open(path)
	if err != nil {
		return nil, err
	}
	file, ok := f.(*os.File)
	if !ok {
		utils.CloseQuietly(f)
		return nil, fmt.Errorf("open %q: not a host file", path)
	}
	return file, nil
}

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
	defer utils.CloseQuietly(f)
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

// Resolve returns the absolute host path of ref (workspace-relative, such as
// "maestro/x.md", or absolute) when the agent may read it through the file
// tools, and an error otherwise (or when it does not exist).
func (r *Reader) Resolve(ref string) (string, error) {
	if !r.restrict && !filepath.IsAbs(ref) {
		// Unrestricted reads go straight to the host: anchor the path at
		// the workspace, not the process's working directory.
		ref = filepath.Join(r.workspace, ref)
	}
	if _, err := r.Stat(ref); err != nil {
		return "", err
	}
	if !filepath.IsAbs(ref) {
		ref = filepath.Join(r.workspace, ref)
	}
	abs, err := filepath.Abs(ref)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", ref, err)
	}
	return abs, nil
}

// Allowed reports whether the agent may read the absolute host path abs
// through the file tools: nil when it may, the refusal otherwise. A path
// inside the workspace, which may be reached through a symbolic link (abs is
// often a link's resolved target), is checked as a workspace path; a path in
// an allowed folder is checked as it is.
func (r *Reader) Allowed(abs string) error {
	_, err := r.Stat(r.virtualPath(filepath.Clean(abs)))
	return err
}

// virtualPath maps an absolute path inside the workspace (by its configured
// path or its resolved one) to the path the file tools use for it; any other
// path is returned as it is.
func (r *Reader) virtualPath(path string) string {
	if r.workspace != "" && r.restrict {
		if rel, ok := under(r.workspace, path); ok {
			return rel
		}
	}
	return path
}

// under returns path relative to root when it lies inside root, compared
// both as given and with root's symbolic links resolved.
func under(root, path string) (string, bool) {
	roots := []string{filepath.Clean(root)}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != roots[0] {
		roots = append(roots, resolved)
	}
	for _, rt := range roots {
		if rel, err := filepath.Rel(rt, path); err == nil && filepath.IsLocal(rel) {
			return rel, true
		}
	}
	return "", false
}
