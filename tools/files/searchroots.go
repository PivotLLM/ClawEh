package files

import (
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// defaultSearchRoots are the folders file_glob and file_search_lines search
// when no path is given: every workspace folder the agent may read, relative
// to the workspace. With a read scope that is its folders (files/, skills/
// and the always-readable ones) plus any allowed folder inside the workspace
// (maestro/ when Maestro is on); folders that do not exist are left out.
// Allowed folders outside the workspace are not included. Without a read
// scope (reads are workspace-wide) it is the workspace itself, ".".
func defaultSearchRoots(sysFs fileSystem, workspace string) []string {
	if workspace == "" || len(readScopeSubdirs) == 0 {
		return []string{"."}
	}
	names := slices.Clone(readScopeSubdirs)
	ws := filepath.Clean(workspace)
	for _, f := range allowedFoldersForWorkspace(workspace) {
		if rel, err := filepath.Rel(ws, filepath.Clean(f)); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			names = append(names, filepath.ToSlash(rel))
		}
	}
	var out []string
	for _, n := range names {
		n = path.Clean(filepath.ToSlash(n))
		if slices.Contains(out, n) {
			continue
		}
		if info, err := sysFs.Stat(n); err == nil && info.IsDir() {
			out = append(out, n)
		}
	}
	return out
}

// rootsLabel names the searched folders in a result header.
func rootsLabel(roots []string) string {
	return strings.Join(roots, ", ")
}

// parentGitignores returns the .gitignore patterns that apply to dir from its
// parent folders, and dir's path below the repository root as gitignore path
// components, so the patterns are anchored where git anchors them. The
// repository root is the nearest ancestor holding .git. The walk up stops at
// the first ancestor the agent may not read; with no repository root found
// before that, or when those patterns ignore dir itself (the agent named an
// ignored folder on purpose), it returns no patterns and no components, and
// only the .gitignore files inside dir apply.
func (t *SearchFilesTool) parentGitignores(dir string) ([]gitignore.Pattern, []string) {
	dir = path.Clean(filepath.ToSlash(dir))
	// Ancestors of dir, nearest first, up to the repository root.
	var ancestors []string
	repoFound := false
	for cur := dir; ; {
		parent := path.Dir(cur)
		if parent == cur {
			break // "/" or "."
		}
		if info, err := t.sysFs.Stat(parent); err != nil || !info.IsDir() {
			break // outside what the agent may read
		}
		ancestors = append(ancestors, parent)
		if _, err := t.sysFs.Lstat(path.Join(parent, ".git")); err == nil {
			repoFound = true
			break
		}
		cur = parent
	}
	if !repoFound {
		return nil, nil
	}
	repoRoot := ancestors[len(ancestors)-1]
	var below []string // dir's components below the repository root
	if rel := strings.TrimPrefix(strings.TrimPrefix(dir, repoRoot), "/"); repoRoot == "." {
		below = strings.Split(dir, "/")
	} else {
		below = strings.Split(rel, "/")
	}
	var patterns []gitignore.Pattern
	for i, anc := range slices.Backward(ancestors) { // the repository root first
		domain := below[:len(below)-1-i]
		if data := t.readGitignore(path.Join(anc, gitignoreFile)); data != nil {
			patterns = append(patterns, parseGitignore(data, domain)...)
		}
	}
	if len(patterns) > 0 && gitignore.NewMatcher(patterns).Match(below, true) {
		return nil, nil
	}
	return patterns, below
}
