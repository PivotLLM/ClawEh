package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Direct tests for whitelistFs methods to ensure coverage.

func TestWhitelistFsDirect_ReadFile_Matching(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wl_read.txt")
	os.WriteFile(path, []byte("whitelist read content"), 0o644)

	pattern := regexp.MustCompile(regexp.QuoteMeta(dir))
	wl := &whitelistFs{
		sandbox:  &sandboxFs{workspace: t.TempDir()},
		patterns: []*regexp.Regexp{pattern},
	}

	data, err := wl.ReadFile(path)
	if err != nil {
		t.Fatalf("whitelistFs.ReadFile() error = %v", err)
	}
	if string(data) != "whitelist read content" {
		t.Errorf("content = %q", string(data))
	}
}

func TestWhitelistFsDirect_ReadFile_NonMatching(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sandbox_read.txt")
	os.WriteFile(path, []byte("sandbox content"), 0o644)

	pattern := regexp.MustCompile(`^/no/match$`)
	wl := &whitelistFs{
		sandbox:  &sandboxFs{workspace: workspace},
		patterns: []*regexp.Regexp{pattern},
	}

	data, err := wl.ReadFile(path)
	if err != nil {
		t.Fatalf("whitelistFs.ReadFile() error = %v", err)
	}
	if string(data) != "sandbox content" {
		t.Errorf("content = %q", string(data))
	}
}

func TestWhitelistFsDirect_WriteFile_Matching(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wl_write.txt")

	pattern := regexp.MustCompile(regexp.QuoteMeta(dir))
	wl := &whitelistFs{
		sandbox:  &sandboxFs{workspace: t.TempDir()},
		patterns: []*regexp.Regexp{pattern},
	}

	err := wl.WriteFile(path, []byte("written by whitelist"))
	if err != nil {
		t.Fatalf("whitelistFs.WriteFile() error = %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "written by whitelist" {
		t.Errorf("content = %q", string(data))
	}
}

func TestWhitelistFsDirect_WriteFile_NonMatching(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sandbox_write.txt")

	pattern := regexp.MustCompile(`^/no/match$`)
	wl := &whitelistFs{
		sandbox:  &sandboxFs{workspace: workspace},
		patterns: []*regexp.Regexp{pattern},
	}

	err := wl.WriteFile(path, []byte("sandbox write"))
	if err != nil {
		t.Fatalf("whitelistFs.WriteFile() error = %v", err)
	}
}

func TestWhitelistFsDirect_ReadDir_Matching(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "wl_file.txt"), []byte("x"), 0o644)

	pattern := regexp.MustCompile(regexp.QuoteMeta(dir))
	wl := &whitelistFs{
		sandbox:  &sandboxFs{workspace: t.TempDir()},
		patterns: []*regexp.Regexp{pattern},
	}

	entries, err := wl.ReadDir(dir)
	if err != nil {
		t.Fatalf("whitelistFs.ReadDir() error = %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name() == "wl_file.txt" {
			found = true
		}
	}
	if !found {
		t.Error("expected wl_file.txt in readdir results")
	}
}

func TestWhitelistFsDirect_ReadDir_NonMatching(t *testing.T) {
	workspace := t.TempDir()
	os.WriteFile(filepath.Join(workspace, "sandbox_dir.txt"), []byte("x"), 0o644)

	pattern := regexp.MustCompile(`^/no/match$`)
	wl := &whitelistFs{
		sandbox:  &sandboxFs{workspace: workspace},
		patterns: []*regexp.Regexp{pattern},
	}

	entries, err := wl.ReadDir(workspace)
	if err != nil {
		t.Fatalf("whitelistFs.ReadDir() error = %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name() == "sandbox_dir.txt" {
			found = true
		}
	}
	if !found {
		t.Error("expected sandbox_dir.txt in readdir results")
	}
}

func TestWhitelistFsDirect_Open_Matching(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wl_open.txt")
	os.WriteFile(path, []byte("open content"), 0o644)

	pattern := regexp.MustCompile(regexp.QuoteMeta(dir))
	wl := &whitelistFs{
		sandbox:  &sandboxFs{workspace: t.TempDir()},
		patterns: []*regexp.Regexp{pattern},
	}

	f, err := wl.Open(path)
	if err != nil {
		t.Fatalf("whitelistFs.Open() error = %v", err)
	}
	f.Close()
}

func TestWhitelistFsDirect_Open_NonMatching(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sandbox_open.txt")
	os.WriteFile(path, []byte("open content"), 0o644)

	pattern := regexp.MustCompile(`^/no/match$`)
	wl := &whitelistFs{
		sandbox:  &sandboxFs{workspace: workspace},
		patterns: []*regexp.Regexp{pattern},
	}

	f, err := wl.Open(path)
	if err != nil {
		t.Fatalf("whitelistFs.Open() error = %v", err)
	}
	f.Close()
}

func TestBuildFs_Unrestricted(t *testing.T) {
	fs := buildFs("", false, nil)
	if _, ok := fs.(*hostFs); !ok {
		t.Errorf("buildFs(unrestricted) = %T, want *hostFs", fs)
	}
}

func TestBuildFs_RestrictedNoPatterns(t *testing.T) {
	fs := buildFs(t.TempDir(), true, nil)
	if _, ok := fs.(*sandboxFs); !ok {
		t.Errorf("buildFs(restricted, no patterns) = %T, want *sandboxFs", fs)
	}
}

func TestBuildFs_RestrictedWithPatterns(t *testing.T) {
	pattern := regexp.MustCompile(`/foo`)
	fs := buildFs(t.TempDir(), true, []*regexp.Regexp{pattern})
	if _, ok := fs.(*whitelistFs); !ok {
		t.Errorf("buildFs(restricted, with patterns) = %T, want *whitelistFs", fs)
	}
}

func TestValidatePath_RelativeInWorkspace(t *testing.T) {
	workspace := t.TempDir()

	path, err := validatePath("myfile.txt", workspace, false)
	if err != nil {
		t.Fatalf("validatePath() error = %v", err)
	}
	if !strings.HasSuffix(path, "myfile.txt") {
		t.Errorf("path = %q, want to end with 'myfile.txt'", path)
	}
}

func TestValidatePath_AbsoluteOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()

	_, err := validatePath(outside, workspace, true)
	if err == nil {
		t.Error("validatePath() should error for absolute path outside workspace")
	}
}
