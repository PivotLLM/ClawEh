package files

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MountSpec is a resolved external mount: a top-level name → absolute directory.
type MountSpec struct {
	Name string
	Path string
}

// mountsByWorkspace holds per-agent mounts keyed by the agent's workspace path.
// Mirrors the readScopeSubdirs pattern but is keyed per workspace because mounts
// are per-agent. Set at tool-construction time by the provider.
var (
	mountsMu          sync.RWMutex
	mountsByWorkspace = map[string][]MountSpec{}
)

// SetMountsForWorkspace installs the external mounts for an agent's workspace.
// Passing nil/empty clears them.
func SetMountsForWorkspace(workspace string, mounts []MountSpec) {
	mountsMu.Lock()
	defer mountsMu.Unlock()
	if workspace == "" {
		return
	}
	if len(mounts) == 0 {
		delete(mountsByWorkspace, workspace)
		return
	}
	cp := make([]MountSpec, len(mounts))
	copy(cp, mounts)
	mountsByWorkspace[workspace] = cp
}

func mountsForWorkspace(workspace string) []MountSpec {
	mountsMu.RLock()
	defer mountsMu.RUnlock()
	return mountsByWorkspace[workspace]
}

// withMounts wraps inner with a mountFs when the workspace has mounts. mountFs is
// the outermost layer so mount paths (`<name>/...`) are intercepted before the
// workspace read/write scopes, and resolved into the mount's own sandbox.
func withMounts(workspace string, inner fileSystem) fileSystem {
	if specs := mountsForWorkspace(workspace); len(specs) > 0 {
		return newMountFs(inner, specs)
	}
	return inner
}

// mountFs routes paths whose first component matches a mount name into that
// mount's sandboxed tree (rooted at the external path, no `..` escape); all other
// paths delegate to inner.
type mountFs struct {
	inner  fileSystem
	mounts map[string]*sandboxFs // name -> sandbox rooted at the mount path
}

func newMountFs(inner fileSystem, specs []MountSpec) *mountFs {
	m := &mountFs{inner: inner, mounts: make(map[string]*sandboxFs, len(specs))}
	for _, s := range specs {
		m.mounts[s.Name] = &sandboxFs{workspace: s.Path}
	}
	return m
}

// resolve returns the mount sandbox and the path relative to the mount root
// (or "." for the mount root itself), and whether path is a mount path.
func (m *mountFs) resolve(path string) (*sandboxFs, string, bool) {
	p := strings.TrimPrefix(filepath.ToSlash(path), "./")
	name, rest := p, "."
	if i := strings.IndexByte(p, '/'); i >= 0 {
		name, rest = p[:i], p[i+1:]
		if rest == "" {
			rest = "."
		}
	}
	if sb, ok := m.mounts[name]; ok {
		return sb, rest, true
	}
	return nil, "", false
}

func (m *mountFs) ReadFile(path string) ([]byte, error) {
	if sb, rel, ok := m.resolve(path); ok {
		return sb.ReadFile(rel)
	}
	return m.inner.ReadFile(path)
}

func (m *mountFs) WriteFile(path string, data []byte) error {
	if sb, rel, ok := m.resolve(path); ok {
		return sb.WriteFile(rel, data)
	}
	return m.inner.WriteFile(path, data)
}

func (m *mountFs) WriteFileMode(path string, data []byte, mode os.FileMode) error {
	if sb, rel, ok := m.resolve(path); ok {
		return sb.WriteFileMode(rel, data, mode)
	}
	return m.inner.WriteFileMode(path, data, mode)
}

func (m *mountFs) WriteFileExclMode(path string, data []byte, mode os.FileMode) error {
	if sb, rel, ok := m.resolve(path); ok {
		return sb.WriteFileExclMode(rel, data, mode)
	}
	return m.inner.WriteFileExclMode(path, data, mode)
}

func (m *mountFs) Remove(path string) error {
	if sb, rel, ok := m.resolve(path); ok {
		return sb.Remove(rel)
	}
	return m.inner.Remove(path)
}

func (m *mountFs) ReadDir(path string) ([]os.DirEntry, error) {
	if sb, rel, ok := m.resolve(path); ok {
		if rel == "." { // the mount root itself
			return os.ReadDir(sb.workspace)
		}
		return sb.ReadDir(rel)
	}
	return m.inner.ReadDir(path)
}

func (m *mountFs) Stat(path string) (os.FileInfo, error) {
	if sb, rel, ok := m.resolve(path); ok {
		if rel == "." {
			return os.Stat(sb.workspace)
		}
		return sb.Stat(rel)
	}
	return m.inner.Stat(path)
}

func (m *mountFs) Open(path string) (fs.File, error) {
	if sb, rel, ok := m.resolve(path); ok {
		if rel == "." {
			return os.Open(sb.workspace)
		}
		return sb.Open(rel)
	}
	return m.inner.Open(path)
}

