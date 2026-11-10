package files

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/PivotLLM/ClawEh/tools"
)

// allowedByWorkspace holds, per agent workspace, the absolute folders the
// agent's file tools may use besides the workspace scope: its allowed folders
// and, with Maestro on, <workspace>/maestro. Set at tool-construction time by
// the provider; Reader consults the same table.
var (
	allowedMu          sync.RWMutex
	allowedByWorkspace = map[string][]string{}
)

// SetAllowedFoldersForWorkspace installs the allowed folders (absolute,
// existing directories) for an agent's workspace. Passing nil/empty clears
// them.
func SetAllowedFoldersForWorkspace(workspace string, folders []string) {
	allowedMu.Lock()
	defer allowedMu.Unlock()
	if workspace == "" {
		return
	}
	if len(folders) == 0 {
		delete(allowedByWorkspace, workspace)
		return
	}
	allowedByWorkspace[workspace] = append([]string(nil), folders...)
}

func allowedFoldersForWorkspace(workspace string) []string {
	allowedMu.RLock()
	defer allowedMu.RUnlock()
	return allowedByWorkspace[workspace]
}

// withAllowedFolders wraps inner with an allowedFs when the workspace has
// allowed folders, or when the agent is confined (so a workspace path given in
// its resolved form is the workspace's). allowedFs is the outermost layer, so a
// path inside an allowed folder is served from that folder, not by the
// workspace read/write scopes below.
func withAllowedFolders(workspace string, restrict bool, inner fileSystem) fileSystem {
	folders := allowedFoldersForWorkspace(workspace)
	if len(folders) == 0 && (!restrict || workspace == "") {
		return inner
	}
	a := &allowedFs{inner: inner, workspace: workspace, restrict: restrict}
	for _, f := range folders {
		a.roots = append(a.roots, newAllowedRoot(f))
	}
	if workspace != "" {
		a.ws = newAllowedRoot(workspace)
	}
	return a
}

// allowedRoot is an allowed folder as configured and with its links resolved.
type allowedRoot struct {
	path     string
	resolved string
}

func newAllowedRoot(p string) allowedRoot {
	p = filepath.Clean(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	r := allowedRoot{path: p, resolved: p}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		r.resolved = resolved
	}
	return r
}

// contains reports whether p (absolute, clean) is the root or inside it, as
// configured or resolved.
func (r allowedRoot) contains(p string) bool {
	return within(p, r.path) || within(p, r.resolved)
}

func within(p, root string) bool {
	return p == root || isWithinWorkspace(p, root)
}

// allowedFs serves a path that lies inside one of the agent's allowed folders
// from that folder, sandboxed to it (os.Root: nothing below it can lead out),
// and hands every other path to inner unchanged.
//
// A path is placed by where it resolves (links followed). The workspace's own
// rules always win inside the workspace: a path that resolves there is served
// by inner (with the workspace scopes), even when an allowed folder holds the
// whole workspace, unless it lies in an allowed folder that is itself inside
// the workspace (maestro/). A path that resolves into an allowed folder outside
// the workspace is served there. One that names an allowed folder but leads
// out of every folder the agent may use is refused (when the agent is
// confined). A relative path keeps resolving from the workspace; it reaches an
// allowed folder only when the folder lies in the workspace.
//
// Writes are attempted, never refused by policy: the folder's file permissions
// decide, and a permission error becomes a short refusal.
type allowedFs struct {
	inner     fileSystem
	workspace string
	restrict  bool
	ws        allowedRoot
	roots     []allowedRoot
}

// target is where a path is served: by a folder's sandbox (sb, rel), or by
// inner at path.
type target struct {
	sb   *sandboxFs
	rel  string
	abs  string // the path as the agent named it, absolute (for messages)
	path string // the path inner gets when sb is nil
}

// errOutsideAllowed is the refusal for a path that names an allowed folder but
// leads outside every folder the agent may use.
func errOutsideAllowed(p string) error {
	return tools.Refusal(fmt.Errorf("access denied: %s resolves outside the allowed folders", p))
}

// place decides where path is served. With followLast false the last
// component is not resolved (removing or lstat-ing a link acts on the link).
func (a *allowedFs) place(path string, followLast bool) (target, error) {
	abs, ok := a.absolute(path)
	if !ok {
		return target{path: path}, nil
	}
	lex := a.rootOf(abs)
	if !filepath.IsAbs(path) && (lex < 0 || !a.insideWorkspace(lex)) {
		// A relative path keeps its workspace meaning, unless it names a
		// folder inside the workspace (maestro/).
		return target{path: path}, nil
	}
	resolved := resolveFrom(abs, followLast)
	inWS := a.workspace != "" && a.ws.contains(resolved)
	if i := a.rootOf(resolved); i >= 0 && (!inWS || a.insideWorkspace(i)) {
		root := a.roots[i]
		base := root.resolved
		if !within(resolved, base) {
			base = root.path
		}
		rel, err := filepath.Rel(base, resolved)
		if err != nil {
			return target{}, fmt.Errorf("failed to resolve file path: %w", err)
		}
		return target{sb: &sandboxFs{workspace: base}, rel: rel, abs: abs}, nil
	}
	if inWS {
		// The workspace's: in its configured form, which the scopes below
		// compare against.
		base := a.ws.resolved
		if !within(resolved, base) {
			base = a.ws.path
		}
		rel, err := filepath.Rel(base, resolved)
		if err != nil {
			return target{}, fmt.Errorf("failed to resolve file path: %w", err)
		}
		return target{path: filepath.Join(a.ws.path, rel)}, nil
	}
	if lex < 0 {
		return target{path: path}, nil
	}
	// The path names an allowed folder but a link leads out of every folder
	// the agent may use.
	if a.restrict {
		return target{}, errOutsideAllowed(abs)
	}
	return target{path: abs}, nil
}

// insideWorkspace reports whether allowed folder i lies inside the workspace
// (and is not the workspace itself).
func (a *allowedFs) insideWorkspace(i int) bool {
	if a.workspace == "" {
		return false
	}
	r := a.roots[i]
	for _, p := range []string{r.path, r.resolved} {
		if p != a.ws.path && p != a.ws.resolved && a.ws.contains(p) {
			return true
		}
	}
	return false
}

// absolute returns path as a clean absolute path: an absolute path as given,
// a relative one joined to the workspace when it stays inside it.
func (a *allowedFs) absolute(path string) (string, bool) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), true
	}
	if a.workspace == "" || !filepath.IsLocal(filepath.Clean(path)) {
		return "", false
	}
	return filepath.Join(a.ws.path, path), true
}

// rootOf returns the index of the deepest allowed folder holding p, or -1.
func (a *allowedFs) rootOf(p string) int {
	best, bestLen := -1, -1
	for i, r := range a.roots {
		if r.contains(p) && len(r.path) > bestLen {
			best, bestLen = i, len(r.path)
		}
	}
	return best
}

// resolveFrom resolves the links in abs: in every component, or with
// followLast false in all but the last. Components from the first missing one
// on are appended as given.
func resolveFrom(abs string, followLast bool) string {
	if !followLast {
		dir, base := filepath.Split(abs)
		if base == "" {
			return abs
		}
		return filepath.Join(resolveExisting(filepath.Clean(dir)), base)
	}
	return resolveExisting(abs)
}

func resolveExisting(abs string) string {
	var tail []string
	for cur := abs; ; cur = filepath.Dir(cur) {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...)
		}
		if filepath.Dir(cur) == cur {
			return abs
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
	}
}

// writeErr turns a permission error from a write inside an allowed folder
// into a short refusal naming the path.
func writeErr(t target, err error) error {
	if err != nil && errors.Is(err, fs.ErrPermission) {
		return tools.Refusal(fmt.Errorf("permission denied: %s is read-only for this app", t.abs))
	}
	return err
}

// CheckWrite refuses nothing inside an allowed folder (its permissions
// decide); other paths ask the layer below.
func (a *allowedFs) CheckWrite(path string) error {
	t, err := a.place(path, true)
	if err != nil {
		return err
	}
	if t.sb != nil {
		return nil
	}
	return checkWrite(a.inner, t.path)
}

func (a *allowedFs) ReadFile(path string) ([]byte, error) {
	t, err := a.place(path, true)
	if err != nil {
		return nil, err
	}
	if t.sb != nil {
		return t.sb.ReadFile(t.rel)
	}
	return a.inner.ReadFile(t.path)
}

func (a *allowedFs) WriteFile(path string, data []byte) error {
	t, err := a.place(path, true)
	if err != nil {
		return err
	}
	if t.sb != nil {
		return writeErr(t, t.sb.WriteFile(t.rel, data))
	}
	return a.inner.WriteFile(t.path, data)
}

func (a *allowedFs) WriteFileMode(path string, data []byte, mode os.FileMode) error {
	t, err := a.place(path, true)
	if err != nil {
		return err
	}
	if t.sb != nil {
		return writeErr(t, t.sb.WriteFileMode(t.rel, data, mode))
	}
	return a.inner.WriteFileMode(t.path, data, mode)
}

func (a *allowedFs) WriteFileExclMode(path string, data []byte, mode os.FileMode) error {
	t, err := a.place(path, true)
	if err != nil {
		return err
	}
	if t.sb != nil {
		return writeErr(t, t.sb.WriteFileExclMode(t.rel, data, mode))
	}
	return a.inner.WriteFileExclMode(t.path, data, mode)
}

func (a *allowedFs) Remove(path string) error {
	t, err := a.place(path, false)
	if err != nil {
		return err
	}
	if t.sb != nil {
		return writeErr(t, t.sb.Remove(t.rel))
	}
	return a.inner.Remove(t.path)
}

func (a *allowedFs) MkdirAll(path string) error {
	t, err := a.place(path, true)
	if err != nil {
		return err
	}
	if t.sb != nil {
		return writeErr(t, t.sb.MkdirAll(t.rel))
	}
	return a.inner.MkdirAll(t.path)
}

// isRoot reports whether path is an allowed folder itself, or a root of the
// layer below.
func (a *allowedFs) isRoot(path string) bool {
	t, err := a.place(path, false)
	if err != nil {
		return false
	}
	if t.sb != nil {
		return filepath.Clean(t.rel) == "."
	}
	return isRoot(a.inner, t.path)
}

func (a *allowedFs) ReadDir(path string) ([]os.DirEntry, error) {
	t, err := a.place(path, true)
	if err != nil {
		return nil, err
	}
	if t.sb != nil {
		return t.sb.ReadDir(t.rel)
	}
	return a.inner.ReadDir(t.path)
}

func (a *allowedFs) Stat(path string) (os.FileInfo, error) {
	t, err := a.place(path, true)
	if err != nil {
		return nil, err
	}
	if t.sb != nil {
		return t.sb.Stat(t.rel)
	}
	return a.inner.Stat(t.path)
}

func (a *allowedFs) Lstat(path string) (os.FileInfo, error) {
	t, err := a.place(path, false)
	if err != nil {
		return nil, err
	}
	if t.sb != nil {
		return t.sb.Lstat(t.rel)
	}
	return a.inner.Lstat(t.path)
}

func (a *allowedFs) Open(path string) (fs.File, error) {
	t, err := a.place(path, true)
	if err != nil {
		return nil, err
	}
	if t.sb != nil {
		return t.sb.Open(t.rel)
	}
	return a.inner.Open(t.path)
}
